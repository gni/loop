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
	Registry       *tool.ToolRegistry
	WorkspaceRoot  string

	Tasks         map[string]*Task
	TasksMu       sync.Mutex
	NextTaskId    int
	StreamingTask string

	CurrentStreamBuffer *bytes.Buffer
	CurrentStreamMu     sync.Mutex

	ThinkingSupported      bool
	ThinkingSupportChecked bool

	CurrentWriter  io.Writer
	CurrentContext context.Context

	lastToolOutput         string
	lastToolIsError        bool
	lastToolWasEdit        bool
	LastGenerationDuration time.Duration
	liveBodyStreamed       bool

	TurnStartTime time.Time
	CurrentTheme  style.UITheme
	CurrentLoader TurnLoader

	SpawnedAgents   map[string]bool
	SpawnedAgentsMu sync.RWMutex

	SystemEvents       chan string
	PendingSystemEvent string

	ClearAgentsFunc   func()
	MultiAgentManager any
	DebugLogger       *DebugLogger

	FileObservations *tool.FileObservationTracker
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
	if cfg != nil {
		debugPath = cfg.DebugLogFile
	}

	a := &Agent{
		Config:     cfg,
		ConfigPath: configPath,
		HttpClient: httpClient,
		LLMProvider: &OpenAICompatibleProvider{
			Config:     cfg,
			HttpClient: httpClient,
		},
		McpClients:     make(map[string]*mcpClient),
		McpStartErrors: make(map[string]error),
		Registry:       tool.NewToolRegistry(),
		WorkspaceRoot:  absWorkspace,
		Tasks:          make(map[string]*Task),
		NextTaskId:     1,
		SpawnedAgents:  make(map[string]bool),
		SystemEvents:   make(chan string, 100),
		DebugLogger:    NewDebugLogger(absWorkspace, debugPath),
		FileObservations: tool.NewFileObservationTracker(),
	}

	// Register built-in tools
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

	// Only register local executable plugins from the "plugins" directory in the workspace
	if !a.Config.DisableLocalPlugins {
		pluginsDir := filepath.Join(absWorkspace, "plugins")
		_ = tool.RegisterPlugins(a.Registry, pluginsDir)
	}

	return a
}

func (a *Agent) RecordRead(absPath string, data []byte) {
	if a != nil && a.FileObservations != nil {
		a.FileObservations.RecordRead(absPath, data)
	}
}

func (a *Agent) CheckMutationAllowed(absPath string, isEdit bool) error {
	if a != nil && a.FileObservations != nil {
		return a.FileObservations.CheckMutationAllowed(absPath, isEdit)
	}
	return nil
}

func (a *Agent) RecordMutation(absPath string, data []byte) {
	if a != nil && a.FileObservations != nil {
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

// ApplyConfig replaces the live configuration and refreshes the built-in LLM
// provider so the next request uses the new endpoint, credentials, and model.
func (a *Agent) ApplyConfig(cfg *config.Config) {
	if a == nil || cfg == nil {
		return
	}

	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()

	a.Config = cfg
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
			Config:     cfg,
			HttpClient: httpClient,
		}
	}
	a.ThinkingSupported = false
	a.ThinkingSupportChecked = false

	if a.DebugLogger != nil {
		a.DebugLogger.Close()
	}
	a.DebugLogger = NewDebugLogger(a.WorkspaceRoot, cfg.DebugLogFile)
	a.ForceSystemPromptUpdate = true
}

func (a *Agent) GetWorkspaceRoot() string {
	return a.WorkspaceRoot
}

func (a *Agent) Context() context.Context {
	if a.CurrentContext != nil {
		return a.CurrentContext
	}
	return context.Background()
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
		a.ForceSystemPromptUpdate = true
	}
	return a.ActiveSkills
}

func (a *Agent) GetLiveWriter() io.Writer {
	return a.CurrentWriter
}

func (a *Agent) SetLiveBodyStreamed(streamed bool) {
	a.liveBodyStreamed = streamed
}

func (a *Agent) DidStreamLiveBody() bool {
	return a.liveBodyStreamed
}

func (a *Agent) AskUser(question string, options []tool.AskUserOption, recommended string) (string, error) {
	if a.Config != nil {
		if a.Config.AskUserMode == "auto_recommended" {
			if recommended != "" {
				return recommended, nil
			}
			if len(options) > 0 {
				return options[0].Label, nil
			}
			return "Confirmed", nil
		} else if a.Config.AskUserMode == "disabled" {
			return "", fmt.Errorf("ask_user tool is disabled by configuration")
		}
	}

	var optStrings []string
	for _, opt := range options {
		optStrings = append(optStrings, opt.Label)
	}

	w := a.CurrentWriter
	if w == nil {
		w = os.Stdout
	} else {
		w = unwrapWriter(w)
	}
	theme := a.CurrentTheme

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

	// Clear all existing plugins (tools prefixed with "plugin__")
	a.Registry.UnregisterPrefix("plugin__")

	// Re-register local plugins from the workspace
	pluginsDir := filepath.Join(a.WorkspaceRoot, "plugins")
	return tool.RegisterPlugins(a.Registry, pluginsDir)
}

func (a *Agent) HasSubagent(name string) bool {
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return a.SpawnedAgents[name]
}

