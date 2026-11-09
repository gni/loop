package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	domainsession "loop/pkg/domain/session"
	"loop/pkg/resources"
	"loop/pkg/ui/style"
)

var ErrBusy = errors.New("session is already running; use steer or follow_up")

type Options struct {
	Config        *config.Config
	ConfigPath    string
	Workspace     string
	SessionID     string
	SessionName   string
	Resume        bool
	NoSession     bool
	KeepSelection bool
	Store         *db.JSONLStore
	HTTPClient    *http.Client
	Output        io.Writer
	Events        func(agent.Event) error
	Tools         []string
	Inquirer      func(string, []tool.AskUserOption, string) (string, error)
}

type State struct {
	ID        string          `json:"id"`
	Name      string          `json:"name,omitempty"`
	Workspace string          `json:"workspace"`
	Running   bool            `json:"running"`
	Closed    bool            `json:"closed"`
	Model     string          `json:"model"`
	Provider  string          `json:"provider"`
	Messages  []db.Message    `json:"messages,omitempty"`
	Steering  []string        `json:"steering"`
	FollowUp  []string        `json:"follow_up"`
	Todos     []tool.TodoItem `json:"todos"`
}

type Session struct {
	Agent     *agent.Agent
	Manager   *swarm.MultiAgentManager
	Store     *db.JSONLStore
	ID        string
	Workspace string
	mu        sync.Mutex
	runMu     sync.Mutex
	messages  []db.Message
	steering  []string
	followUp  []string
	cancel    context.CancelFunc
	idle      chan struct{}
	closeDone chan struct{}
	closeErr  error
	closed    bool
	running   bool
	options   Options
	hooks     []resources.Hook
	loaded    resources.PackageResources
}

func New(options Options) (*Session, error) { return NewContext(context.Background(), options) }

// NewContext permits cancellation during configuration, plugin and MCP startup.
// After construction, the session keeps its own lifetime until Close.
func NewContext(ctx context.Context, options Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.SessionID != "" {
		if err := domainsession.ValidateID(options.SessionID); err != nil {
			return nil, err
		}
	}
	if options.SessionName != "" && !options.NoSession {
		options.SessionName = strings.TrimSpace(options.SessionName)
		if err := domainsession.ValidateName(options.SessionName); err != nil {
			return nil, err
		}
	}
	options.Tools = slices.Clone(options.Tools)
	if options.ConfigPath == "" {
		options.ConfigPath = filepath.Join(resources.AgentDirectory(""), "config.json")
	}
	// Manual cd changes the process cwd. Keep the trust store and session
	// resources anchored to the config selected when the session started.
	if absolute, err := filepath.Abs(options.ConfigPath); err != nil {
		return nil, err
	} else {
		options.ConfigPath = absolute
	}
	workspace := options.Workspace
	if workspace == "" {
		workspace = "."
	}
	workspace, err := config.CanonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	cfg := options.Config
	if cfg == nil {
		cfg, err = config.LoadWorkspaceConfigContext(ctx, options.ConfigPath, workspace, config.WorkspaceTrusted(options.ConfigPath, workspace))
	} else {
		cfg, err = config.Clone(cfg)
	}
	if err != nil {
		return nil, err
	}
	agentDirectory := resources.AgentDirectory(options.ConfigPath)
	cfg.SkillDirectories = append(cfg.SkillDirectories, filepath.Join(agentDirectory, "skills"))
	cfg.PluginDirectories = append(cfg.PluginDirectories, filepath.Join(agentDirectory, "plugins"))
	cfg.ExtensionDirectories = append(cfg.ExtensionDirectories, filepath.Join(agentDirectory, "extensions"))
	packageDirs := []string{agentDirectory}
	if cfg.ProjectResources {
		packageDirs = append(packageDirs, filepath.Join(workspace, ".loop"))
	}
	loaded, err := resources.LoadPackages(packageDirs...)
	if err != nil {
		return nil, err
	}
	if err := applyResources(cfg, resources.PackageResources{}, loaded); err != nil {
		return nil, err
	}
	if err := cfg.ValidateSettings(); err != nil {
		return nil, err
	}
	store := options.Store
	if !options.NoSession && store == nil {
		store, err = db.NewJSONLStore(filepath.Join(resources.AgentDirectory(options.ConfigPath), "sessions"))
		if err != nil {
			return nil, err
		}
	}
	if options.NoSession {
		store = nil
	}
	if store != nil {
		store = store.ForWorkspace(workspace)
	}
	id := options.SessionID
	if !options.NoSession && id == "" && options.Resume {
		id, err = store.LatestForWorkspaceContext(ctx, workspace)
		if err != nil {
			return nil, err
		}
	}
	if id == "" {
		id = db.NewUUID()
	}
	var todos []tool.TodoItem
	var restoredMessages []db.Message
	var restoredName string
	if store != nil && store.HasMessages(id) {
		entries, readErr := store.EntriesContext(ctx, id)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, readErr
		}
		todos, err = restoreSettingsFromEntries(entries, cfg, &options.Tools, options.KeepSelection)
		if err != nil {
			return nil, err
		}
		restoredMessages = db.ProjectMessages(entries)
		restoredName = sessionNameFromEntries(entries)
	}
	client := options.HTTPClient
	if client == nil {
		tls, err := config.GetTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		var transport *http.Transport
		if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
			transport = base.Clone()
		} else {
			transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
		}
		transport.TLSClientConfig = tls
		client = &http.Client{Transport: transport}
	}
	s := &Session{Store: store, ID: id, Workspace: workspace, options: options, idle: make(chan struct{}), hooks: loaded.Hooks, loaded: loaded}
	close(s.idle)
	s.Agent = agent.NewAgentAt(cfg, options.ConfigPath, client, workspace)
	s.Agent.ConfigControl = s
	stopConstruction := context.AfterFunc(ctx, s.Agent.CancelLifetime)
	defer stopConstruction()
	s.Agent.SessionMessages = s.MessagesSnapshot
	if store != nil {
		s.Agent.Store = store
	}
	constructed := false
	defer func() {
		if !constructed {
			_ = s.Agent.Dispose(context.Background())
		}
	}()
	if cfg.LocalPlugins {
		if err := s.Agent.ReloadPluginsContext(ctx); err != nil {
			return nil, err
		}
	}
	s.Agent.Inquirer = options.Inquirer
	s.Agent.ToolSelection = options.Tools
	s.Agent.Ephemeral = options.NoSession
	s.Agent.SetActiveSessionID(id)
	s.Agent.RestoreSessionName(id, restoredName)
	s.Agent.RunSession = s.RunInteractive
	s.Agent.SyncSessionState = s.SyncInteractive
	s.Agent.Bus = agent.NewEventBus()
	s.Agent.Bus.On(s.permissionHook)
	s.Agent.EventSink = s.event
	s.Agent.NextInput = s.takeSteering
	s.Agent.TransformContext = s.transformContext
	s.Agent.ReloadResources = s.Reload
	if len(todos) > 0 {
		if err := s.Agent.SetTodos(todos); err != nil {
			return nil, err
		}
	}
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	s.Manager = swarm.NewMultiAgentManager(s.Agent, output, style.GetTheme("plain"))
	if err := s.Manager.SetSessionDirectory(filepath.Join(resources.AgentDirectory(options.ConfigPath), "agents", config.WorkspaceKey(workspace), id), options.NoSession); err != nil {
		return nil, err
	}
	s.Agent.MultiAgentManager = s.Manager
	s.Agent.DisposeAgents = s.Manager.Close
	if store != nil {
		s.messages, err = s.Agent.RecoverSessionMessages(id, restoredMessages)
		if err != nil {
			return nil, err
		}
	}
	skills, err := agent.LoadSkillsFromDirs(s.Agent.ConfiguredSkillDirs()...)
	if err != nil {
		return nil, err
	}
	s.Agent.SetActiveSkills(skills)
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	if err := s.Agent.PersistenceError(); err != nil {
		return nil, err
	}
	if cfg.MaxSubagentDepth > 0 {
		if err := s.Manager.LoadSavedAgents(); err != nil {
			return nil, err
		}
	}
	if err := s.validateTools(options.Tools); err != nil {
		return nil, err
	}
	if err := s.Agent.StartMCPServers(cfg.MCPServers); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if eventErr := s.eventContext(ctx, agent.Event{Type: "integration_error", Output: err.Error(), IsError: true}); eventErr != nil {
			return nil, eventErr
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.SessionName != "" && store != nil {
		if err := s.Agent.SetSessionName(ctx, options.SessionName); err != nil {
			return nil, err
		}
	}
	if err := s.eventContext(ctx, agent.Event{Type: "session_start", SessionID: s.ID}); err != nil {
		return nil, err
	}
	stopConstruction()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	constructed = true
	return s, nil
}

func (s *Session) storageID() string {
	if s.Store == nil {
		return ""
	}
	return s.ID
}

func (s *Session) MessagesSnapshot() []db.Message {
	s.Agent.MessagesMu.RLock()
	defer s.Agent.MessagesMu.RUnlock()
	return message.CloneAll(s.messages)
}

func (s *Session) event(event agent.Event) error {
	ctx := s.Agent.Context()
	if event.Type == "session_end" || event.Type == "agent_end" {
		ctx = context.WithoutCancel(ctx)
	}
	return s.eventContext(ctx, event)
}

func (s *Session) eventContext(ctx context.Context, event agent.Event) error {
	// A plan update must survive interruption before the model turn ends.
	if event.Type == "tool_execution_end" && event.ToolCall != nil && event.ToolCall.Function.Name == "todo" && !event.IsError && !strings.HasPrefix(event.SessionID, "subagent:") {
		if err := s.persistSettings(); err != nil {
			return err
		}
	}
	event.Version = 1
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if event.SessionID == "" {
		event.SessionID = s.ID
	}
	s.mu.Lock()
	hooks := append([]resources.Hook(nil), s.hooks...)
	s.mu.Unlock()
	for _, hook := range hooks {
		if hook.Event == event.Type && hook.Event != "tool_call" && hook.Event != "tool_result" {
			if _, err := resources.RunHook(ctx, hook, event); err != nil {
				return err
			}
		}
	}
	if s.options.Events != nil {
		return s.options.Events(event)
	}
	return nil
}

type Outcome struct {
	Result agent.RunResult
	Err    error
}

// lockIdle reserves an open, idle session for a mutation. The caller must unlock runMu.
func (s *Session) lockIdle() error {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.runMu.Unlock()
		return fmt.Errorf("session is closed")
	}
	return nil
}

func (s *Session) reserve(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.runMu.TryLock() {
		return nil, ErrBusy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Checking closure and publishing the running state must be atomic with Close.
	if s.closed {
		s.runMu.Unlock()
		return nil, fmt.Errorf("session is closed")
	}
	if err := ctx.Err(); err != nil {
		s.runMu.Unlock()
		return nil, err
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.idle = make(chan struct{})
	return ctx, nil
}

func (s *Session) Start(ctx context.Context, prompt string) (<-chan Outcome, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return nil, err
	}
	done := make(chan Outcome, 1)
	go func() {
		result, err := s.run(ctx, prompt)
		done <- Outcome{Result: result, Err: err}
		close(done)
	}()
	return done, nil
}

func (s *Session) Run(ctx context.Context, prompt string) (agent.RunResult, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return agent.RunResult{}, err
	}
	return s.run(ctx, prompt)
}

func (s *Session) run(ctx context.Context, prompt string) (agent.RunResult, error) {
	var output io.Writer = io.Discard
	if s.Agent.Config.Nostop.Enabled() && s.options.Output != nil {
		output = s.options.Output
	}
	return s.runWith(ctx, prompt, output, style.GetTheme("plain"), true)
}

func (s *Session) RunInteractive(ctx context.Context, w io.Writer, messages *[]db.Message, prompt string, tools []string, theme style.UITheme, id string) (agent.RunResult, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return agent.RunResult{}, err
	}
	result, err := s.runPrepared(ctx, prompt, w, theme, false, func() error { return s.adoptInteractiveContext(ctx, *messages, tools, id) })
	*messages = s.MessagesSnapshot()
	return result, err
}

func (s *Session) adoptInteractive(messages []db.Message, tools []string, id string) error {
	return s.adoptInteractiveContext(s.Agent.Context(), messages, tools, id)
}

func (s *Session) adoptInteractiveContext(ctx context.Context, messages []db.Message, tools []string, id string) error {
	if id != "" {
		if err := domainsession.ValidateID(id); err != nil {
			return err
		}
	}
	switching := id != "" && id != s.ID
	// Accepted foreground history still belongs to the current session during
	// cancellation. Switching resources requires a live operation context.
	if switching {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var restoredName string
	if switching {
		if s.Agent.PersistenceError() != nil {
			if err := s.checkpointMessages(ctx); err != nil {
				return err
			}
		}
		if err := s.persistSettingsIfActiveContext(ctx); err != nil {
			return err
		}
		cfg, err := s.Configuration()
		if err != nil {
			return err
		}
		var todos []tool.TodoItem
		if s.Store != nil {
			var entries []domainsession.Record
			if s.Store.HasMessages(id) {
				entries, err = s.Store.EntriesContext(ctx, id)
				if err != nil {
					return err
				}
			}
			todos, err = restoreSettingsFromEntries(entries, cfg, &tools, false)
			if err != nil {
				return err
			}
			restoredName = sessionNameFromEntries(entries)
		}
		prepared, err := s.Manager.PrepareSessionDirectory(filepath.Join(resources.AgentDirectory(s.Agent.ConfigPath), "agents", config.WorkspaceKey(s.Workspace), id), s.options.NoSession)
		if err != nil {
			return err
		}
		defer prepared.Close()
		applyErr := s.Agent.ApplyConfig(cfg)
		if !agent.ConfigApplied(applyErr) {
			return applyErr
		}
		if applyErr != nil {
			s.Agent.SetPendingSystemEvent(applyErr.Error())
		}
		if err := prepared.Commit(); err != nil {
			return err
		}
		if err := s.Agent.SetTodos(todos); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		s.ID = id
	}
	s.Agent.SetActiveSessionID(s.ID)
	if switching {
		s.Agent.RestoreSessionName(s.ID, restoredName)
	}
	s.Agent.MessagesMu.Lock()
	s.messages = message.CloneAll(messages)
	s.Agent.MessagesMu.Unlock()
	s.options.Tools = slices.Clone(tools)
	s.Agent.ToolSelection = s.options.Tools
	return nil
}

func (s *Session) runWith(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool) (agent.RunResult, error) {
	return s.runPrepared(ctx, prompt, w, theme, noninteractive, nil)
}

func (s *Session) runPrepared(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool, setup func() error) (result agent.RunResult, runErr error) {
	runID := s.ID
	defer func() {
		if err := s.persistSettingsIfActiveContext(ctx); err != nil {
			runErr = errors.Join(runErr, err)
		}
		result = result.WithError(runErr)
		snapshot := result.Clone()
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.running = false
		s.cancel = nil
		idle := s.idle
		s.mu.Unlock()
		s.runMu.Unlock()
		close(idle)
		if err := s.eventContext(context.WithoutCancel(ctx), agent.Event{Type: "agent_settled", SessionID: runID, Result: &snapshot}); err != nil {
			runErr = errors.Join(runErr, err)
			result = result.WithError(runErr)
		}
	}()
	if setup != nil {
		if err := setup(); err != nil {
			return result, err
		}
		runID = s.ID
	}
	for {
		if err := ctx.Err(); err != nil {
			return agent.RunResult{Status: agent.RunCancelled, Error: err.Error()}, err
		}
		attachmentSource, literal := resources.AttachmentSource(ctx, prompt)
		preExpanded := resources.IsPreExpandedPrompt(ctx, prompt)
		attached, blocks, err := resources.Attachments(attachmentSource, s.Workspace)
		if err != nil {
			return result, err
		}
		attachmentSuffix := strings.TrimPrefix(attached, attachmentSource)

		transformed, err := s.transformInput(ctx, prompt)
		if err != nil {
			result.Status = agent.RunBlocked
			result.Error = err.Error()
			return result, err
		}
		expanded := transformed
		if !preExpanded && (!literal || strings.HasPrefix(strings.TrimSpace(attachmentSource), "/")) {
			expanded, err = resources.ExpandPrompt(transformed, resources.PromptDirectories(resources.AgentDirectory(s.Agent.ConfigPath), s.Workspace, s.Agent.Config.ProjectResources, s.Agent.Config.PromptDirectories))
			if err != nil {
				return result, err
			}
		}
		expanded += attachmentSuffix
		s.Agent.InputBlocks = blocks
		s.Agent.ToolSelection = s.options.Tools
		result, err = s.Agent.RunAgentLoop(ctx, w, &s.messages, expanded, s.options.Tools, theme, noninteractive, s.storageID())
		if err != nil {
			return result, err
		}
		s.mu.Lock()
		if len(s.steering) > 0 {
			prompt = s.steering[0]
			s.steering = s.steering[1:]
		} else if len(s.followUp) > 0 {
			prompt = s.followUp[0]
			s.followUp = s.followUp[1:]
		} else {
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
	}
	return result, nil
}

func (s *Session) Queue(prompt string, followUp bool) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("queued prompt is empty")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if len(s.steering)+len(s.followUp) >= 256 {
		s.mu.Unlock()
		return fmt.Errorf("input queue is full")
	}
	if followUp {
		s.followUp = append(s.followUp, prompt)
	} else {
		s.steering = append(s.steering, prompt)
	}
	s.mu.Unlock()
	return s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.Status()})
}

func (s *Session) takeSteering() []db.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steering) == 0 {
		return nil
	}
	prompt := s.steering[0]
	s.steering = s.steering[1:]
	return []db.Message{{Role: "user", Content: prompt}}
}

func (s *Session) ClearQueue() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	s.steering = nil
	s.followUp = nil
	s.mu.Unlock()
	return s.event(agent.Event{Type: "queue_update", SessionID: s.ID})
}

func (s *Session) Wait(ctx context.Context) error {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) Abort(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.Wait(ctx)
}

// Close starts cleanup once. A caller timing out does not abandon cleanup,
// and later callers can still wait for its completion and receive its result.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closeDone == nil {
		s.closed = true
		s.closeDone = make(chan struct{})
		cancel := s.cancel
		go s.finishClose()
		if cancel != nil {
			cancel()
		}
	}
	done := s.closeDone
	s.mu.Unlock()
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	default:
	}
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) finishClose() {
	if s.Agent.CancelLifetime != nil {
		s.Agent.CancelLifetime()
	}
	_ = s.Wait(context.Background())
	s.runMu.Lock()
	// Storage contention must not keep processes alive or block exit forever.
	disposeErr := s.Agent.Dispose(context.Background())
	flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	var checkpointErr error
	if s.Agent.PersistenceError() != nil {
		checkpointErr = s.checkpointMessages(flushCtx)
	}
	settingsErr := s.persistSettingsIfActiveContext(flushCtx)
	cancel()
	s.runMu.Unlock()
	// A blocked event consumer must not keep processes or connections alive.
	err := errors.Join(checkpointErr, settingsErr, disposeErr, s.event(agent.Event{Type: "session_end", SessionID: s.ID}))
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
}
