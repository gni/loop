package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
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
	lastGenerationDuration time.Duration
	liveBodyStreamed       bool

	TurnStartTime time.Time
	CurrentTheme  style.UITheme
	CurrentLoader TurnLoader

	SpawnedAgents   map[string]bool
	SpawnedAgentsMu sync.RWMutex

	SystemEvents       chan string
	PendingSystemEvent string

	ClearAgentsFunc   func()
	MultiAgentManager *MultiAgentManager
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

func (a *Agent) SafePath(inputPath string) (string, error) {
	if inputPath == "" {
		return a.WorkspaceRoot, nil
	}

	target := inputPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(a.WorkspaceRoot, target)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	cleanRoot := filepath.Clean(a.WorkspaceRoot)
	cleanTarget := filepath.Clean(absTarget)

	if cleanTarget == cleanRoot {
		return cleanTarget, nil
	}

	// Surgical security checks: block modifying active configuration file or session databases
	if a.ConfigPath != "" {
		absConfig, errConfig := filepath.Abs(a.ConfigPath)
		if errConfig == nil {
			cleanConfig := filepath.Clean(absConfig)
			if cleanTarget == cleanConfig {
				return "", fmt.Errorf("security violation: modifying the active configuration file is not allowed")
			}
			cleanSessionsDir := filepath.Clean(filepath.Join(filepath.Dir(absConfig), "sessions"))
			if cleanTarget == cleanSessionsDir || strings.HasPrefix(cleanTarget, cleanSessionsDir+string(filepath.Separator)) {
				return "", fmt.Errorf("security violation: modifying session database files is not allowed")
			}
		}
	}

	// Surgical allowlist: allow writing to global memory files
	home, err := os.UserHomeDir()
	if err == nil {
		globalLoop := filepath.Clean(filepath.Join(home, ".loop", "LOOP.md"))
		if cleanTarget == globalLoop {
			return cleanTarget, nil
		}
	}

	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	// Resilient fallback: if cleanTarget does not exist, check if inputPath repeated the workspace root folder name
	// (for example calling 'tests/fastapi_boilerplate' when workspace root is already '/workspace/tests').
	if _, err := os.Stat(cleanTarget); os.IsNotExist(err) {
		rootBase := filepath.Base(cleanRoot)
		slashInput := filepath.ToSlash(inputPath)
		if strings.HasPrefix(slashInput, rootBase+"/") {
			stripped := strings.TrimPrefix(slashInput, rootBase+"/")
			altTarget := filepath.Clean(filepath.Join(cleanRoot, stripped))
			if _, altErr := os.Stat(altTarget); altErr == nil {
				cleanTarget = altTarget
			}
		}
	}

	if !strings.HasPrefix(cleanTarget, prefix) {
		if strings.TrimSpace(inputPath) == ".." {
			return "", fmt.Errorf("path '..' is outside workspace root '%s'. Workspace root is the top-level directory", a.WorkspaceRoot)
		}
		return "", fmt.Errorf("security violation: path '%s' escapes workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	// Canonical symlink containment verification (sandbox policy)
	evalRoot := cleanRoot
	if realRoot, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		evalRoot = filepath.Clean(realRoot)
	}
	evalPrefix := evalRoot
	if !strings.HasSuffix(evalPrefix, string(filepath.Separator)) {
		evalPrefix += string(filepath.Separator)
	}

	var evalTarget string
	if realTarget, err := filepath.EvalSymlinks(cleanTarget); err == nil {
		evalTarget = filepath.Clean(realTarget)
	} else if os.IsNotExist(err) {
		parent := filepath.Dir(cleanTarget)
		if realParent, pErr := filepath.EvalSymlinks(parent); pErr == nil {
			evalTarget = filepath.Clean(filepath.Join(realParent, filepath.Base(cleanTarget)))
		}
	}

	if evalTarget != "" && evalTarget != evalRoot && !strings.HasPrefix(evalTarget, evalPrefix) {
		return "", fmt.Errorf("security violation: path '%s' resolves via symlink outside workspace root '%s'", inputPath, a.WorkspaceRoot)
	}

	return cleanTarget, nil
}

func (a *Agent) CompressHistory(
	ctx context.Context,
	messages *[]db.Message,
	sessionID string,
	theme style.UITheme,
	w io.Writer,
) {
	keepMsgCount := 4
	if len(*messages) <= keepMsgCount+2 {
		keepMsgCount = 2
	}

	keepIdx := len(*messages) - keepMsgCount
	if keepIdx <= 1 {
		fmt.Fprintln(w, "conversation is already compact (too few messages to compress).")
		return
	}

	toCompress := (*messages)[1:keepIdx]

	var transcriptBuilder strings.Builder
	readFilesMap := make(map[string]bool)
	modifiedFilesMap := make(map[string]bool)

	for _, m := range toCompress {
		if m.Role == "user" {
			transcriptBuilder.WriteString(fmt.Sprintf("User: %s\n\n", m.Content))
		} else if m.Role == "assistant" {
			if m.Content != "" {
				transcriptBuilder.WriteString(fmt.Sprintf("Agent: %s\n\n", m.Content))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					transcriptBuilder.WriteString(fmt.Sprintf("Agent requested tool call: %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
					var fileArg struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &fileArg) == nil && fileArg.Path != "" {
						if tc.Function.Name == "read" {
							readFilesMap[fileArg.Path] = true
						} else if tc.Function.Name == "write" || tc.Function.Name == "edit" {
							modifiedFilesMap[fileArg.Path] = true
						}
					}
				}
			}
		} else if m.Role == "tool" {
			out := m.Content
			if len(out) > 400 {
				out = out[:400] + "... (truncated)"
			}
			transcriptBuilder.WriteString(fmt.Sprintf("Tool Output (%s): %s\n\n", m.Name, out))
		}
	}

	var readFiles, modifiedFiles []string
	for f := range readFilesMap {
		readFiles = append(readFiles, f)
	}
	for f := range modifiedFilesMap {
		modifiedFiles = append(modifiedFiles, f)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)

	transcript := transcriptBuilder.String()
	summaryPrompt := fmt.Sprintf(
		"You are a technical context compression engine. Summarize the following developer-agent conversation transcript into a dense, high-signal technical log.\n\n"+
			"Strict Requirements:\n"+
			"1. User Goals & Constraints: Exact requirements, architectural preferences, and explicit rules stated by the user.\n"+
			"2. Actions & Findings: Search results, files located or inspected, and diagnostic findings.\n"+
			"3. File Modifications: Exact paths modified or created, and key symbols added or updated.\n"+
			"4. Current State: What is complete, what failed, and immediate pending tasks.\n"+
			"5. No pleasantries, preambles, or filler. Output only the structured technical summary.\n\n"+
			"Transcript:\n%s",
		transcript,
	)

	summaryMsgs := []db.Message{
		{
			Role:    "user",
			Content: summaryPrompt,
		},
	}

	infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, infoStyle.Render("[System: Context usage threshold reached. Compressing older conversation history...]"))

	// The summarizer has no renderer, so its chunks are discarded. The drain loop
	// must outlive the call and exit when the channel is closed, otherwise every
	// compression leaks one goroutine that holds the channel forever.
	dummyChan := make(chan StreamChunk, 100)
	drained := make(chan struct{})
	go func() {
		for range dummyChan {
			// Discard summarizer stream chunks
		}
		close(drained)
	}()

	summaryAssistantMsg, err := a.StreamChatCompletions(ctx, summaryMsgs, []string{}, dummyChan)
	close(dummyChan)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}
	if err != nil {
		warnStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s Failed to compress conversation context: %v\n", warnStyle.Render("WARNING:"), err)
		return
	}

	summaryText := summaryAssistantMsg.Content
	var fileOpsHeader string
	if len(readFiles) > 0 || len(modifiedFiles) > 0 {
		fileOpsHeader = fmt.Sprintf("\n- Files Read: %s\n- Files Modified: %s\n", strings.Join(readFiles, ", "), strings.Join(modifiedFiles, ", "))
	}
	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf("[System: Below is a summary of the earlier conversation history:%s\n%s]", fileOpsHeader, summaryText),
	}

	keptMessages := make([]db.Message, 0, len((*messages)[keepIdx:]))
	for _, m := range (*messages)[keepIdx:] {
		if m.Role == "tool" && m.Name != "read" && m.Name != "write" && m.Name != "edit" && len(m.Content) > 30000 {
			m.Content = m.Content[:30000] + "... (truncated for context optimization)"
		}
		keptMessages = append(keptMessages, m)
	}

	newMessages := make([]db.Message, 0, 2+len(keptMessages))
	newMessages = append(newMessages, (*messages)[0])  // Keep system prompt
	newMessages = append(newMessages, summaryMsg)      // Add summary
	newMessages = append(newMessages, keptMessages...) // Add latest messages

	if sessionID != "" {
		_ = db.RewriteSession(sessionID, newMessages)
	}
	*messages = newMessages

	compressedStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	fmt.Fprintf(w, "%s\n\n", compressedStyle.Render(fmt.Sprintf("context compressed · freed %d messages", keepIdx-1)))
}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	if a.Config.BeforeToolHook == "" {
		return true, ""
	}

	cmd := exec.Command("bash", "-c", a.Config.BeforeToolHook)
	cmd.Env = os.Environ()

	payload := map[string]string{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
		}
		if reason == "" {
			reason = err.Error()
		}
		return false, reason
	}
	return true, ""
}

func (a *Agent) runAfterToolHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	if a.Config.AfterToolHook == "" {
		return output, toolErr
	}

	cmd := exec.Command("bash", "-c", a.Config.AfterToolHook)
	cmd.Env = os.Environ()

	errStr := ""
	if toolErr != nil {
		errStr = toolErr.Error()
	}

	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
		"output":       output,
		"error":        errStr,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	hookOutput := stdout.String()
	if hookOutput != "" {
		return hookOutput, nil
	}
	return output, toolErr
}

func (a *Agent) HasSubagent(name string) bool {
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return a.SpawnedAgents[name]
}

// GetDebugLogPath returns the file path of the debug log if enabled.
func (a *Agent) GetDebugLogPath() string {
	if a == nil || a.DebugLogger == nil {
		return ""
	}
	return a.DebugLogger.FilePath()
}

// DebugLogUserCommand records a submitted prompt to the debug log.
func (a *Agent) DebugLogUserCommand(sessionID, prompt string) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogUserCommand(sessionID, prompt)
}

// DebugLogLLMRequest records outgoing messages and tools payload to the debug log.
func (a *Agent) DebugLogLLMRequest(sessionID string, iter int, model, endpoint string, messages []db.Message, tools []tool.Tool) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogLLMRequest(sessionID, iter, model, endpoint, messages, tools)
}

// DebugLogLLMResponse records the LLM completion and proposed tool calls.
func (a *Agent) DebugLogLLMResponse(sessionID string, iter int, msg *db.Message, duration time.Duration) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogLLMResponse(sessionID, iter, msg, duration)
}

// DebugLogToolExecution records tool execution input, output, errors, and re-read detection.
func (a *Agent) DebugLogToolExecution(sessionID string, iter int, toolName string, arguments string, output string, err error, duration time.Duration) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogToolExecution(sessionID, iter, toolName, arguments, output, err, duration)
}

// DebugLogRepetition records repetition circuit breaker warnings to the debug log.
func (a *Agent) DebugLogRepetition(sessionID string, toolName string, arguments string, count int, detail string) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogRepetition(sessionID, toolName, arguments, count, detail)
}

// GetEffectiveContextLimit calculates the dynamic context window limit considering
// backend detection, active prompt token requirements, and configured boundaries.
func (a *Agent) GetEffectiveContextLimit(currentPromptTokens int) int {
	if a == nil {
		return 128000
	}
	serverLimit := 0
	a.LLMProviderMu.RLock()
	if detector, ok := a.LLMProvider.(ContextLimitDetector); ok {
		serverLimit = detector.GetDetectedContextLimit()
	}
	a.LLMProviderMu.RUnlock()

	autoAdapt := true
	minWindow := 32768
	configuredLimit := 128000
	if a.Config != nil {
		autoAdapt = a.Config.AutoAdaptContext
		if a.Config.MinContextWindow > 0 {
			minWindow = a.Config.MinContextWindow
		}
		if a.Config.ContextWindowLimit > 0 {
			configuredLimit = a.Config.ContextWindowLimit
		}
	}
	return GetAdaptiveContextLimit(currentPromptTokens, serverLimit, configuredLimit, minWindow, autoAdapt)
}
