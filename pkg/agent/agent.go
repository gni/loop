package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

type StreamRenderer interface {
	Write(content string)
	WriteReasoning(content string)
	Flush()
	HasOutput() bool
	StartToolCall(toolName string, toolCallIndex int)
	SetToolTarget(target string, toolCallIndex int)
	WriteToolCall(content string)
	GetToolTitleLineNumber(index int) int
	DidStreamToolBody(index int) bool
	CompleteToolCall(index int, toolName string, toolArgs string, isError bool)
	GetReasoningDuration() float64
	SetPrompt(prompt string)
}

type TurnLoader interface {
	Pause()
	Resume()
}

type SubagentCancellationDecision uint8

const (
	SubagentCancellationContinue SubagentCancellationDecision = iota
	SubagentCancellationSkipCurrent
	SubagentCancellationStopAll
)

type AgentUI interface {
	DrawStatusBar(w io.Writer, theme style.UITheme)
	DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string)
	NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) StreamRenderer
	SetCollapseStatus(collapsed bool)
	UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool)
	UpdatePlanStatus(completed, total int)
	DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string)
	AskForApproval(w io.Writer, theme style.UITheme) (bool, bool)
	AskUserQuestion(w io.Writer, theme style.UITheme, question string, options []string, recommended string) (string, error)
	AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) SubagentCancellationDecision
	RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string)
	RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool)
	SetCursorHidden(hidden bool)
	RenderGenerationError(w io.Writer, message string, theme style.UITheme)
}

type Agent struct {
	UI             AgentUI
	LLMProvider    LLMProvider
	LLMProviderMu  sync.RWMutex
	Config         *config.Config
	ConfigPath     string
	HttpClient     *http.Client
	ActiveSkills   []Skill
	McpClients     map[string]*mcpClient
	McpClientsMu   sync.Mutex
	McpStartErrors map[string]error

	mcpGeneration int
	mcpPending    map[string]config.MCPServerConfig

	Registry      *tool.ToolRegistry
	WorkspaceRoot string

	Tasks         map[string]*Task
	TasksMu       sync.Mutex
	NextTaskId    int
	StreamingTask string

	CurrentStreamBuffer *bytes.Buffer
	CurrentStreamMu     sync.Mutex

	stateMu        sync.Mutex
	currentWriter  io.Writer
	currentContext context.Context
	currentTheme   style.UITheme

	lastToolOutput         string
	lastToolIsError        bool
	lastToolWasEdit        bool
	lastGenerationDuration time.Duration
	liveBodyStreamed       bool
	droppedChunks          int64
	lastTurnDropped        int64

	TurnStartTime time.Time
	CurrentLoader TurnLoader

	SpawnedAgents   map[string]bool
	SpawnedAgentsMu sync.RWMutex

	SystemEvents       chan string
	PendingSystemEvent string

	ClearAgentsFunc   func()
	MultiAgentManager any
	DebugLogger       *DebugLogger

	FileObservations *tool.FileObservationTracker
	Guided           *GuidedController
	Todos            []tool.TodoItem
	TodosMu          sync.RWMutex

	ForceSystemPromptUpdate bool
}

func NewAgent(cfg *config.Config, configPath string, httpClient *http.Client) *Agent {
	cwd, err := os.Getwd()
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

	a := &Agent{
		Config:     cfg,
		ConfigPath: configPath,
		HttpClient: httpClient,
		LLMProvider: &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: httpClient,
		},
		McpClients:       make(map[string]*mcpClient),
		McpStartErrors:   make(map[string]error),
		mcpPending:       make(map[string]config.MCPServerConfig),
		Registry:         tool.NewToolRegistry(),
		WorkspaceRoot:    absWorkspace,
		Tasks:            make(map[string]*Task),
		NextTaskId:       1,
		SpawnedAgents:    make(map[string]bool),
		SystemEvents:     make(chan string, 100),
		DebugLogger:      NewDebugLogger(absWorkspace, debugPath),
		FileObservations: tool.NewFileObservationTracker(),
		Guided:           NewGuidedController(guidedEnabled),
	}

	a.Registry.UnknownToolHook = a.ResolveUnknownTool
	a.Registry.Register(tool.NewLoadMCPTool())

	if provider, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok {
		provider.ChunkDropObserver = a.recordDroppedChunk
	}

	a.Registry.Register(tool.NewBashTool())
	a.Registry.Register(tool.NewReadTool())
	a.Registry.Register(tool.NewWriteTool())
	a.Registry.Register(tool.NewEditTool())
	a.Registry.Register(tool.NewGrepTool())
	a.Registry.Register(tool.NewListTool())
	a.Registry.Register(tool.NewFindTool())
	a.Registry.Register(tool.NewLoadSkillTool())
	a.Registry.Register(tool.NewTaskStatusTool())
	a.Registry.Register(tool.NewTaskKillTool())
	a.Registry.Register(tool.NewTodoTool())
	a.Registry.Register(tool.NewAskUserTool())

	if a.Config != nil && !a.Config.DisableLocalPlugins {
		pluginsDir := filepath.Join(absWorkspace, "plugins")
		_ = tool.RegisterPlugins(a.Registry, pluginsDir)
	}

	if a.Config != nil && len(a.Config.DisabledTools) > 0 {
		a.Registry.Disable(a.Config.DisabledTools...)
	}

	return a
}

func (a *Agent) ensureGuided() *GuidedController {
	if a == nil {
		return nil
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.Guided == nil {
		enabled := a.Config != nil && a.Config.GuidedMode
		a.Guided = NewGuidedController(enabled)
	}
	return a.Guided
}

func (a *Agent) IsGuidedMode() bool {
	if a == nil {
		return false
	}
	if a.Guided != nil {
		return a.Guided.IsEnabled()
	}
	return a.Config != nil && a.Config.GuidedMode
}

func (a *Agent) SetGuidedMode(enabled bool) {
	if a == nil {
		return
	}
	if a.Config != nil {
		a.Config.GuidedMode = enabled
		if enabled {
			a.Config.AutoApprove = false
			a.Config.ApprovalAlwaysAnswer = false
			a.Config.ParallelToolCalls = false
		}
	}
	a.ensureGuided().SetEnabled(enabled)
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) RollbackChanges() ([]string, error) {
	if a == nil {
		return nil, nil
	}
	return a.ensureGuided().Rollback(a.FileObservations)
}

func (a *Agent) HasSnapshot(absPath string) bool {
	if a == nil {
		return false
	}
	return a.ensureGuided().HasSnapshot(absPath)
}

func (a *Agent) DiscardSnapshot(absPath string) {
	if a == nil {
		return
	}
	a.ensureGuided().DiscardSnapshot(absPath)
}

func (a *Agent) RecordRead(absPath string, data []byte) {
	if a != nil && a.FileObservations != nil {
		a.FileObservations.RecordRead(absPath, data)
	}
}

func (a *Agent) CheckMutationAllowed(absPath string, isEdit bool) error {
	if a == nil {
		return nil
	}
	if a.FileObservations != nil {
		if err := a.FileObservations.CheckMutationAllowed(absPath, isEdit); err != nil {
			return err
		}
	}
	a.ensureGuided().CaptureFileSnapshot(absPath)
	return nil
}

func (a *Agent) RecordMutation(absPath string, data []byte) {
	if a == nil {
		return
	}
	a.ensureGuided().CaptureFileSnapshot(absPath)
	if a.FileObservations != nil {
		a.FileObservations.RecordMutation(absPath, data)
	}
}

func (a *Agent) GetTodos() []tool.TodoItem {
	if a == nil {
		return nil
	}
	a.TodosMu.RLock()
	defer a.TodosMu.RUnlock()
	res := make([]tool.TodoItem, len(a.Todos))
	copy(res, a.Todos)
	return res
}

func (a *Agent) SetTodos(todos []tool.TodoItem) error {
	if a == nil {
		return nil
	}
	a.TodosMu.Lock()
	a.Todos = make([]tool.TodoItem, len(todos))
	copy(a.Todos, todos)
	completed := 0
	for _, t := range todos {
		if t.Status == "completed" {
			completed++
		}
	}
	total := len(todos)
	a.TodosMu.Unlock()

	if a.UI != nil {
		a.UI.UpdatePlanStatus(completed, total)
	}
	return nil
}

func (a *Agent) ApplyConfig(cfg *config.Config) {
	if a == nil || cfg == nil {
		return
	}

	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()

	a.Config = cfg
	a.ensureGuided().SetEnabled(cfg.GuidedMode)

	switch provider := a.LLMProvider.(type) {
	case nil:
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: a.HttpClient,
		}
	case *OpenAICompatibleProvider:
		httpClient := provider.HttpClient
		if httpClient == nil {
			httpClient = a.HttpClient
		}
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:            cfg,
			HttpClient:        httpClient,
			ChunkDropObserver: a.recordDroppedChunk,
		}
	}

	if p, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok {
		p.ResetThinkingCapabilities()
	}

	if a.DebugLogger != nil {
		a.DebugLogger.Close()
	}
	a.DebugLogger = NewDebugLogger(a.WorkspaceRoot, cfg.DebugLogFile)

	if a.Registry != nil {
		a.Registry.EnableAll()
		if len(cfg.DisabledTools) > 0 {
			a.Registry.Disable(cfg.DisabledTools...)
		}
	}
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) DisableTools(names ...string) {
	if a == nil || a.Registry == nil {
		return
	}
	a.Registry.Disable(names...)
	if a.Config != nil {
		a.Config.DisabledTools = a.Registry.GetDisabledTools()
	}
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) EnableTools(names ...string) {
	if a == nil || a.Registry == nil {
		return
	}
	a.Registry.Enable(names...)
	if a.Config != nil {
		a.Config.DisabledTools = a.Registry.GetDisabledTools()
	}
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) EnableAllTools() {
	if a == nil || a.Registry == nil {
		return
	}
	a.Registry.EnableAll()
	if a.Config != nil {
		a.Config.DisabledTools = nil
	}
	a.stateMu.Lock()
	a.ForceSystemPromptUpdate = true
	a.stateMu.Unlock()
}

func (a *Agent) IsToolDisabled(name string) bool {
	if a == nil || a.Registry == nil {
		return false
	}
	return a.Registry.IsDisabled(name)
}

func (a *Agent) GetDisabledTools() []string {
	if a == nil || a.Registry == nil {
		return nil
	}
	return a.Registry.GetDisabledTools()
}

func (a *Agent) GetWorkspaceRoot() string {
	return a.WorkspaceRoot
}

func (a *Agent) Context() context.Context {
	a.stateMu.Lock()
	ctx := a.currentContext
	a.stateMu.Unlock()
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func (a *Agent) CurrentWriter() io.Writer {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.currentWriter
}

func (a *Agent) CurrentTheme() style.UITheme {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.currentTheme
}

func (a *Agent) LastGenerationDuration() time.Duration {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.lastGenerationDuration
}

func (a *Agent) recordDroppedChunk() {
	if a == nil {
		return
	}
	a.stateMu.Lock()
	a.droppedChunks++
	a.lastTurnDropped++
	a.stateMu.Unlock()
}

func (a *Agent) DroppedChunks() int64 {
	if a == nil {
		return 0
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.droppedChunks
}

func (a *Agent) TurnDroppedChunks() int64 {
	if a == nil {
		return 0
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.lastTurnDropped
}

func (a *Agent) SetTurnState(w io.Writer, ctx context.Context, theme style.UITheme) {
	a.stateMu.Lock()
	a.currentWriter = w
	a.currentContext = ctx
	a.currentTheme = theme
	a.lastTurnDropped = 0
	a.stateMu.Unlock()
}

func (a *Agent) ClearTurnState() {
	a.stateMu.Lock()
	a.currentWriter = nil
	a.currentContext = nil
	a.currentTheme = style.UITheme{}
	a.stateMu.Unlock()
}

func (a *Agent) SetCurrentWriter(w io.Writer) {
	a.stateMu.Lock()
	a.currentWriter = w
	a.stateMu.Unlock()
}

func (a *Agent) SetLastGenerationDuration(d time.Duration) {
	a.stateMu.Lock()
	a.lastGenerationDuration = d
	a.stateMu.Unlock()
}

func (a *Agent) GetActiveSkills() []tool.Skill {
	return a.ActiveSkills
}

func (a *Agent) ReloadSkills() []tool.Skill {
	if a == nil || a.Config == nil {
		if a != nil {
			return a.ActiveSkills
		}
		return nil
	}
	dirs := SkillSearchDirs(a.Config.SkillsDir, a.WorkspaceRoot)
	if len(dirs) == 0 {
		return a.ActiveSkills
	}
	skills, err := LoadSkillsFromDirs(dirs...)
	if err == nil && (len(skills) > 0 || len(a.ActiveSkills) == 0) {
		a.ActiveSkills = skills
		a.stateMu.Lock()
		a.ForceSystemPromptUpdate = true
		a.stateMu.Unlock()
	}
	return a.ActiveSkills
}

func (a *Agent) GetLiveWriter() io.Writer {
	return a.CurrentWriter()
}

func (a *Agent) GetChatWriter() io.Writer {
	if a == nil {
		return os.Stderr
	}
	if a.UI != nil {
		if wh, ok := a.UI.(interface{ GetPPWriter() io.Writer }); ok {
			if w := wh.GetPPWriter(); w != nil {
				return w
			}
		}
	}
	if w := a.CurrentWriter(); w != nil {
		return w
	}
	return os.Stderr
}

func (a *Agent) SetLiveBodyStreamed(streamed bool) {
	a.stateMu.Lock()
	a.liveBodyStreamed = streamed
	a.stateMu.Unlock()
}

func (a *Agent) DidStreamLiveBody() bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.liveBodyStreamed
}

func (a *Agent) AskUser(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if a.Config != nil && !a.IsGuidedMode() {
		switch a.Config.AskUserMode {
		case "auto_recommended":
			if recommended != "" {
				return recommended, nil
			}
			if len(options) > 0 {
				return options[0].Label, nil
			}
			return "Confirmed", nil
		case "disabled":
			return "", fmt.Errorf("ask_user tool is disabled by configuration")
		case "always_ask":
			if a.UI == nil {
				return "", fmt.Errorf("ask_user_mode=always_ask requires an interactive UI; no UI attached")
			}
		}
	}

	var optStrings []string
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

	if a.UI != nil {
		return a.UI.AskUserQuestion(w, theme, question, optStrings, recommended)
	}

	if recommended != "" {
		return recommended, nil
	}
	if len(options) > 0 {
		return options[0].Label, nil
	}
	return "Confirmed", nil
}

func (a *Agent) IsPersistentBashEnabled() bool {
	if a.Config == nil {
		return true
	}
	return a.Config.PersistentBash
}

func (a *Agent) IsAtomicWritesEnabled() bool {
	if a.Config == nil {
		return true
	}
	return a.Config.AtomicWrites
}

func (a *Agent) ReloadPlugins() error {
	if a.Config.DisableLocalPlugins {
		return fmt.Errorf("local plugins are disabled for this workspace (run with trust to enable)")
	}

	a.Registry.UnregisterPrefix("plugin__")

	pluginsDir := filepath.Join(a.WorkspaceRoot, "plugins")
	return tool.RegisterPlugins(a.Registry, pluginsDir)
}

func (a *Agent) HasSubagent(name string) bool {
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return a.SpawnedAgents[name]
}