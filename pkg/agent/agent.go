package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/auth"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/resources"
	"loop/pkg/ui/style"
)

type Agent struct {
	providerState
	mcpState
	backgroundTasks
	ReloadResources     func() error
	Lifetime            context.Context
	CancelLifetime      context.CancelFunc
	TransformContext    func(context.Context, []db.Message) ([]db.Message, error)
	RunSession          func(context.Context, io.Writer, *[]db.Message, string, []string, style.UITheme, string) (RunResult, error)
	SyncSessionState    func([]db.Message, []string, string, bool) error
	SelectSessionBranch func(context.Context, string) ([]db.Message, error)
	SessionMessages     func() []db.Message
	MessagesMu          sync.RWMutex
	Ephemeral           bool
	ToolSelection       []string
	Timing              *TimingRecorder
	Bus                 *EventBus
	Credentials         *auth.Store
	InputBlocks         []message.ContentBlock
	Store               session.Store
	Inquirer            func(string, []tool.AskUserOption, string) (string, error)
	EventSink           func(Event) error
	DisposeAgents       func()
	persistenceErr      error
	persistenceFirstErr error
	persistenceFailures uint64
	NextInput           func() []db.Message
	UI                  AgentUI
	Config              *config.Config
	ConfigControl       ConfigurationController
	ConfigPath          string
	ActiveSkills        []Skill

	Registry      *tool.ToolRegistry
	WorkspaceRoot string

	CurrentStreamBuffer *bytes.Buffer
	// CurrentHistorySnapshot supplies the history owned by the running loop.
	// Invoke on the foreground presentation goroutine, alongside stream replay.
	CurrentHistorySnapshot func() []db.Message
	// CurrentStreamReplay renders semantic, not terminal-byte, output from
	// the uncommitted model step. Its renderer locks its own snapshot.
	CurrentStreamReplay func(io.Writer, bool, style.UITheme)
	CurrentStreamMu     sync.Mutex
	// OnStreamProgress runs on the foreground stream consumer after a chunk
	// has been rendered. It may request a safe redraw of the active turn.
	OnStreamProgress func() bool

	stateMu            sync.Mutex
	activeSessionID    string
	sessionGeneration  uint64
	sessionAutoApprove bool
	sessionName        string
	sessionNameKnown   bool
	sessionNameDirty   bool
	liveToolWriter     io.Writer
	currentWriter      io.Writer
	currentContext     context.Context
	currentTheme       style.UITheme

	lastToolOutput         string
	lastToolIsError        bool
	lastToolWasEdit        bool
	lastGenerationDuration time.Duration
	liveBodyStreamed       bool
	droppedChunks          int64
	lastTurnDropped        int64
	emittedMemoryErrors    map[string]bool

	TurnStartTime time.Time
	CurrentLoader TurnLoader

	SpawnedAgents   map[string]bool
	SpawnedAgentsMu sync.RWMutex

	SystemEvents       chan string
	pendingSystemEvent string

	ClearAgentsFunc   func() error
	MultiAgentManager any
	DebugLogger       *DebugLogger

	FileObservations *tool.FileObservationTracker
	Guided           *GuidedController
	Todos            []tool.TodoItem
	TodosMu          sync.RWMutex

	ForceSystemPromptUpdate bool
}

func NewAgent(cfg *config.Config, configPath string, httpClient *http.Client) *Agent {
	cwd, _ := os.Getwd()
	return NewAgentAt(cfg, configPath, httpClient, cwd)
}

func NewAgentAt(cfg *config.Config, configPath string, httpClient *http.Client, workspace string) *Agent {
	if cfg != nil {
		cfg.NormalizeSafety()
	}
	cwd, err := filepath.Abs(workspace)
	if err != nil {
		cwd = "."
	}
	absWorkspace, _ := filepath.Abs(cwd)

	debugPath := ""
	guidedEnabled := false
	if cfg != nil {
		debugPath = cfg.DebugLogFile
		guidedEnabled = cfg.GuidedMode
	}

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	a := &Agent{
		Lifetime: lifetime, CancelLifetime: cancelLifetime,
		Config:           cfg,
		providerState:    providerState{HTTPClient: httpClient},
		mcpState:         mcpState{MCPClients: make(map[string]*mcpClient), MCPStartErrors: make(map[string]error), mcpPending: make(map[string]config.MCPServerConfig)},
		backgroundTasks:  backgroundTasks{Tasks: make(map[string]*Task), NextTaskID: 1},
		Timing:           NewTimingRecorder(),
		Credentials:      auth.NewStore(filepath.Join(resources.AgentDirectory(configPath), "auth.json")),
		ConfigPath:       configPath,
		Registry:         tool.NewToolRegistry(),
		WorkspaceRoot:    absWorkspace,
		SpawnedAgents:    make(map[string]bool),
		SystemEvents:     make(chan string, 100),
		DebugLogger:      NewDebugLogger(absWorkspace, debugPath),
		FileObservations: tool.NewFileObservationTracker(),
		Guided:           NewGuidedController(guidedEnabled),
	}
	a.LLMProvider = &OpenAICompatibleProvider{
		Config:            cfg,
		HTTPClient:        httpClient,
		ChunkDropObserver: a.recordDroppedChunk,
		ResolveCredential: a.resolveCredential,
	}

	a.FileObservations.SetDisabled(cfg != nil && !cfg.FileObservationGuard)

	a.Registry.UnknownToolHook = a.ResolveUnknownTool
	a.Registry.Register(tool.NewLoadMCPTool())
	a.Registry.Register(&toolSearch{agent: a})
	a.Registry.Register(tool.NewBashTool())
	a.Registry.Register(tool.NewReadTool())
	a.Registry.Register(tool.NewWriteTool())
	a.Registry.Register(tool.NewEditTool())
	a.Registry.Register(tool.NewGrepTool())
	a.Registry.Register(tool.NewListTool())
	a.Registry.Register(tool.NewFindTool())
	a.Registry.Register(tool.NewSymbolTool())
	a.Registry.Register(tool.NewLoadSkillTool())
	a.Registry.Register(tool.NewTaskStatusTool())
	a.Registry.Register(tool.NewTaskKillTool())
	a.Registry.Register(tool.NewTodoTool())
	a.Registry.Register(tool.NewTaskStateTool())
	a.Registry.Register(tool.NewAskUserTool())

	if a.Config != nil && a.Config.LocalPlugins {
		for _, dir := range a.pluginDirs() {
			if err := tool.RegisterPluginsContext(a.Context(), a.Registry, dir); err != nil {
				fmt.Fprintf(os.Stderr, "cannot load plugins: %v\n", err)
			}
		}
	}

	a.syncToolAvailability()
	return a
}

func (a *Agent) markSystemPromptDirty() {
	if a == nil {
		return
	}
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) SyncSessionSystemPrompt(messages *[]db.Message, sessionID string) {
	if a == nil || messages == nil {
		return
	}
	currentSysPrompt := a.GetSystemPrompt()
	a.stateMu.Lock()
	forceUpdate := a.ForceSystemPromptUpdate
	a.ForceSystemPromptUpdate = false
	a.stateMu.Unlock()

	a.MessagesMu.Lock()
	if len(*messages) == 0 {
		*messages = []db.Message{{Role: "system", Content: currentSysPrompt}}
		sysMsg := (*messages)[0]
		a.MessagesMu.Unlock()
		if sessionID != "" && a.HasMessages(sessionID) {
			if err := a.AppendEntry(sessionID, session.Record{Type: "prompt", Message: sysMsg}); err != nil {
				a.recordPersistenceError(err)
			}
		}
		return
	}
	if (*messages)[0].Role == "system" {
		if forceUpdate || (*messages)[0].Content == "" {
			if (*messages)[0].Content != currentSysPrompt {
				(*messages)[0].Content = currentSysPrompt
				sysMsg := (*messages)[0]
				a.MessagesMu.Unlock()
				if sessionID != "" && a.HasMessages(sessionID) {
					if err := a.AppendEntry(sessionID, session.Record{Type: "prompt", Message: sysMsg}); err != nil {
						a.recordPersistenceError(err)
					}
				}
				return
			}
		}
		a.MessagesMu.Unlock()
		return
	}
	a.MessagesMu.Unlock()
}

func (a *Agent) GetWorkspaceRoot() string {
	return a.WorkspaceRoot
}

func (a *Agent) GetActiveSkills() []tool.Skill {
	if a == nil {
		return nil
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	out := make([]tool.Skill, len(a.ActiveSkills))
	copy(out, a.ActiveSkills)
	return out
}

func (a *Agent) HasLoadedSkills() bool {
	if a == nil {
		return false
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.ActiveSkills != nil
}

func (a *Agent) SetActiveSkills(skills []tool.Skill) {
	if a == nil {
		return
	}
	cloned := make([]tool.Skill, len(skills))
	copy(cloned, skills)
	a.stateMu.Lock()
	a.ActiveSkills = cloned
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) SessionMessagesSnapshot() ([]db.Message, bool) {
	if a == nil || a.SessionMessages == nil {
		return nil, false
	}
	return a.SessionMessages(), true
}

func (a *Agent) ConfiguredSkillDirs() []string {
	if a == nil {
		return nil
	}
	return a.skillDirsForConfig(a.Config)
}

func (a *Agent) skillDirsForConfig(cfg *config.Config) []string {
	if a == nil || cfg == nil {
		return nil
	}
	trusted := cfg.ProjectResources
	dirs := SkillSearchDirsForWorkspace(cfg.SkillsDir, a.WorkspaceRoot, trusted)
	seen := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		seen[filepath.Clean(d)] = true
	}
	for _, raw := range cfg.SkillDirectories {
		resolved := resolveSkillDir(raw)
		if resolved == "" {
			continue
		}
		clean := filepath.Clean(resolved)
		if seen[clean] || !a.resourceAllowed(cfg, clean) {
			continue
		}
		seen[clean] = true
		dirs = append(dirs, clean)
	}
	return dirs
}

func (a *Agent) ReloadSkills() []tool.Skill {
	skills, _ := a.ReloadSkillsChecked()
	return skills
}

func (a *Agent) ReloadSkillsChecked() ([]tool.Skill, error) {
	if a == nil || a.Config == nil {
		return a.GetActiveSkills(), nil
	}
	dirs := a.ConfiguredSkillDirs()
	if len(dirs) == 0 {
		a.SetActiveSkills(nil)
		return nil, nil
	}
	skills, err := LoadSkillsFromDirs(dirs...)
	if err != nil {
		return a.GetActiveSkills(), err
	}
	a.SetActiveSkills(skills)
	return a.GetActiveSkills(), nil
}

func defaultAskUserSelection(options []tool.AskUserOption, recommended string) string {
	return tool.DefaultAskUserSelection(options, recommended)
}

func (a *Agent) estimateTurnContext(messages []db.Message, allowlist []string) (int, int) {
	promptTokens, _, _ := a.GetGlobalTokenUsage(messages, allowlist)
	return promptTokens, a.GetEffectiveContextLimit(promptTokens)
}

func (a *Agent) AskUser(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if a.Config != nil && a.Config.AskUserMode == "disabled" {
		return "", fmt.Errorf("ask_user tool is disabled by configuration")
	}
	if a.Inquirer == nil {
		if a.Config != nil && a.Config.AskUserMode == "auto_recommended" {
			return defaultAskUserSelection(options, recommended), nil
		}
		if a.UI == nil {
			if a.Config != nil && a.Config.AskUserMode == "always_ask" {
				return "", fmt.Errorf("ask_user_mode=always_ask requires an interactive UI; no UI attached")
			}
			return defaultAskUserSelection(options, recommended), nil
		}
	}
	return a.askUserInteractively(question, options, recommended)
}

// Approval questions require a live answer regardless of the ask_user mode.
func (a *Agent) askUserInteractively(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if a.Inquirer != nil {
		return a.Inquirer(question, options, recommended)
	}
	if a.UI == nil {
		return "", fmt.Errorf("no interactive approval input is available")
	}

	optStrings := make([]string, 0, len(options))
	for _, opt := range options {
		optStrings = append(optStrings, opt.Label)
	}

	w := a.CurrentWriter()
	if w == nil {
		w = os.Stdout
	} else {
		w = GetScreenWriter(w)
	}
	theme := a.CurrentTheme()

	if a.CurrentLoader != nil {
		a.CurrentLoader.Pause()
		defer a.CurrentLoader.Resume()
	}

	return a.UI.AskUserQuestion(w, theme, question, optStrings, recommended)
}

func (a *Agent) IsPersistentBashEnabled() bool {
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if a.Config == nil {
		return true
	}
	return a.Config.PersistentBash
}

func (a *Agent) IsShellScanDefaultsEnabled() bool {
	if a == nil {
		return false
	}
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	return a.Config != nil && a.Config.ShellScanDefaults
}

func (a *Agent) IsAtomicWritesEnabled() bool {
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if a.Config == nil {
		return true
	}
	return a.Config.AtomicWrites
}

func getPluginDirs(workspaceRoot string) []string {
	return tool.PluginSearchDirs(workspaceRoot)
}

func (a *Agent) ReloadPlugins() error { return a.ReloadPluginsContext(a.Context()) }

// ReloadPluginsContext cancels plugin discovery before replacing active tools.
func (a *Agent) ReloadPluginsContext(ctx context.Context) error {
	if a.Config != nil && !a.Config.LocalPlugins {
		return fmt.Errorf("local plugins are disabled for this workspace (run with trust to enable)")
	}

	staged := tool.NewToolRegistry()
	for _, dir := range a.pluginDirs() {
		if err := tool.RegisterPluginsContext(ctx, staged, dir); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.Registry.ReplacePrefix("plugin__", staged.GetAllExecutors())
	a.markSystemPromptDirty()
	return nil
}

func (a *Agent) HasSubagent(name string) bool {
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return a.SpawnedAgents[name]
}

func (a *Agent) ProjectResourceAllowed(path string) bool { return a.resourceAllowed(a.Config, path) }

func (a *Agent) resourceAllowed(cfg *config.Config, path string) bool {
	if cfg == nil || cfg.ProjectResources {
		return true
	}
	rel, err := filepath.Rel(a.WorkspaceRoot, path)
	return err == nil && (rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (a *Agent) pluginDirs() []string { return a.pluginDirsForConfig(a.Config) }

func (a *Agent) pluginDirsForConfig(cfg *config.Config) []string {
	var allowed []string
	seen := make(map[string]bool)
	for _, dir := range append(getPluginDirs(a.WorkspaceRoot), cfg.PluginDirectories...) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if a.resourceAllowed(cfg, dir) {
			allowed = append(allowed, dir)
		}
	}
	return allowed
}

// IsShellExecutionAllowed gates shell and background tasks on persistent,
// explicit workspace trust. A cwd restriction is not a process sandbox.
func (a *Agent) IsShellExecutionAllowed() bool {
	return a != nil && a.ConfigPath != "" && a.WorkspaceRoot != "" &&
		config.WorkspaceTrusted(a.ConfigPath, a.WorkspaceRoot)
}

func (a *Agent) GetShellTimeout() time.Duration {
	if a != nil {
		a.LLMProviderMu.RLock()
		defer a.LLMProviderMu.RUnlock()
		if a.Config != nil && a.Config.ShellTimeout > 0 {
			return time.Duration(a.Config.ShellTimeout) * time.Second
		}
	}
	return 120 * time.Second
}
