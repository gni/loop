package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/resources"
	"loop/pkg/ui/style"
)

var ErrBusy = errors.New("session is already running; use steer or follow_up")

type Options struct {
	Config        *config.Config
	ConfigPath    string
	Workspace     string
	SessionID     string
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
	Workspace string          `json:"workspace"`
	Running   bool            `json:"running"`
	Closed    bool            `json:"closed"`
	Model     string          `json:"model"`
	Provider  string          `json:"provider"`
	Messages  []db.Message    `json:"messages"`
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
	view      []db.Message
	steering  []string
	followUp  []string
	cancel    context.CancelFunc
	idle      chan struct{}
	closed    bool
	running   bool
	options   Options
	hooks     []resources.Hook
	loaded    resources.PackageResources
}

func New(options Options) (*Session, error) {
	if options.ConfigPath == "" {
		options.ConfigPath = filepath.Join(resources.AgentDirectory(""), "config.json")
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
		cfg, err = config.LoadWorkspaceConfig(options.ConfigPath, workspace, config.WorkspaceTrusted(options.ConfigPath, workspace))
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
	if !cfg.DisableProjectPlugins {
		packageDirs = append(packageDirs, filepath.Join(workspace, ".loop"))
	}
	loaded, err := resources.LoadPackages(packageDirs...)
	if err != nil {
		return nil, err
	}
	if err := applyResources(cfg, resources.PackageResources{}, loaded); err != nil {
		return nil, err
	}
	if err := cfg.ValidatePrompt(); err != nil {
		return nil, err
	}
	if err := cfg.Nostop.Validate(); err != nil {
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
		id, err = store.LatestForWorkspace(workspace)
		if err != nil {
			return nil, err
		}
	}
	if id == "" {
		id = db.NewUUID()
	}
	var todos []tool.TodoItem
	if store != nil {
		todos, err = restoreSettings(store, id, cfg, &options.Tools, options.KeepSelection)
		if err != nil {
			return nil, err
		}
	}
	client := options.HTTPClient
	if client == nil {
		tls, err := config.GetTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		client = &http.Client{Transport: &http.Transport{TLSClientConfig: tls}}
	}
	s := &Session{Store: store, ID: id, Workspace: workspace, options: options, idle: make(chan struct{}), hooks: loaded.Hooks, loaded: loaded}
	close(s.idle)
	s.Agent = agent.NewAgentAt(cfg, options.ConfigPath, client, workspace)
	if store != nil {
		s.Agent.Store = store
	}
	constructed := false
	defer func() {
		if !constructed {
			_ = s.Agent.Dispose(context.Background())
		}
	}()
	s.Agent.Inquirer = options.Inquirer
	s.Agent.ToolSelection = options.Tools
	s.Agent.Ephemeral = options.NoSession
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
	s.Manager.SetSessionDirectory(filepath.Join(resources.AgentDirectory(options.ConfigPath), "agents", config.WorkspaceKey(workspace), id), options.NoSession)
	s.Agent.MultiAgentManager = s.Manager
	s.Agent.DisposeAgents = s.Manager.Close
	if err := s.validateTools(options.Tools); err != nil {
		return nil, err
	}
	if store != nil {
		s.messages, err = store.LoadMessages(id)
		if err != nil {
			return nil, err
		}
	}
	s.Agent.ActiveSkills, err = agent.LoadSkillsFromDirs(append(agent.SkillSearchDirs(cfg.SkillsDir, workspace), cfg.SkillDirectories...)...)
	if err != nil {
		return nil, err
	}
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	if err := s.Agent.PersistenceError(); err != nil {
		return nil, err
	}
	s.view = append([]db.Message(nil), s.messages...)
	if cfg.MaxSubagentDepth > 0 {
		if err := s.Manager.LoadSavedAgents(); err != nil {
			return nil, err
		}
	}
	if err := s.Agent.StartMCPServers(cfg.MCPServers); err != nil {
		s.event(agent.Event{Type: "integration_error", Output: err.Error(), IsError: true})
	}
	if err := s.event(agent.Event{Type: "session_start", SessionID: s.ID}); err != nil {
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

func (s *Session) event(event agent.Event) error {
	// A plan update must survive interruption before the model turn ends.
	if event.Type == "tool_execution_end" && event.ToolCall != nil && event.ToolCall.Function.Name == "todo" && !event.IsError {
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
	if event.Type == "message_end" && event.Message != nil {
		s.view = append(s.view, *event.Message)
	}
	hooks := append([]resources.Hook(nil), s.hooks...)
	s.mu.Unlock()
	for _, hook := range hooks {
		if hook.Event == event.Type && hook.Event != "tool_call" && hook.Event != "tool_result" {
			if _, err := resources.RunHook(s.Agent.Context(), hook, event); err != nil {
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

func (s *Session) reserve(ctx context.Context) (context.Context, error) {
	if !s.runMu.TryLock() {
		return nil, ErrBusy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.runMu.Unlock()
		return nil, fmt.Errorf("session is closed")
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
	result, err := s.runPrepared(ctx, prompt, w, theme, false, func() error { return s.adoptInteractive(*messages, tools, id) })
	*messages = message.CloneAll(s.messages)
	return result, err
}

func (s *Session) adoptInteractive(messages []db.Message, tools []string, id string) error {
	if id != "" && id != s.ID {
		cfg, err := s.Configuration()
		if err != nil {
			return err
		}
		var todos []tool.TodoItem
		if s.Store != nil {
			todos, err = restoreSettings(s.Store, id, cfg, &tools, false)
			if err != nil {
				return err
			}
		}
		if err := s.Manager.SwitchSessionDirectory(filepath.Join(resources.AgentDirectory(s.Agent.ConfigPath), "agents", config.WorkspaceKey(s.Workspace), id), s.options.NoSession); err != nil {
			return err
		}
		s.mu.Lock()
		s.Agent.ApplyConfig(cfg)
		s.mu.Unlock()
		if err := s.Agent.SetTodos(todos); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		s.ID = id
	}
	s.messages = message.CloneAll(messages)
	s.options.Tools = tools
	s.Agent.ToolSelection = tools
	return nil
}

func (s *Session) runWith(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool) (agent.RunResult, error) {
	return s.runPrepared(ctx, prompt, w, theme, noninteractive, nil)
}

func (s *Session) runPrepared(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool, setup func() error) (result agent.RunResult, runErr error) {
	defer func() {
		if err := s.persistSettings(); err != nil {
			runErr = errors.Join(runErr, err)
			result.Status = agent.RunFailed
			result.Error = runErr.Error()
		}
		if runErr != nil && result.Status == "" {
			result.Status = agent.RunFailed
			result.Error = runErr.Error()
		}
		s.mu.Lock()
		s.cancel()
		s.running = false
		s.cancel = nil
		s.view = append([]db.Message(nil), s.messages...)
		idle := s.idle
		s.mu.Unlock()
		if err := s.event(agent.Event{Type: "agent_settled", SessionID: s.ID, Result: &result}); err != nil {
			runErr = errors.Join(runErr, err)
		}
		s.runMu.Unlock()
		close(idle)
	}()
	if setup != nil {
		if err := setup(); err != nil {
			return result, err
		}
	}
	for {
		attachmentSource, literal := resources.AttachmentSource(ctx, prompt)
		transformed, err := s.transformInput(ctx, prompt)
		if err != nil {
			result.Status = agent.RunBlocked
			result.Error = err.Error()
			return result, err
		}
		expanded := transformed
		if !literal || strings.HasPrefix(strings.TrimSpace(attachmentSource), "/") {
			expanded, err = resources.ExpandPrompt(transformed, resources.PromptDirectories(resources.AgentDirectory(s.Agent.ConfigPath), s.Workspace, !s.Agent.Config.DisableProjectPlugins, s.Agent.Config.PromptDirectories))
			if err != nil {
				return result, err
			}
		}
		attachmentInput := expanded
		if literal {
			attachmentInput = attachmentSource
		}
		attached, blocks, err := resources.Attachments(attachmentInput, s.Workspace)
		if err != nil {
			return result, err
		}
		if literal {
			expanded += strings.TrimPrefix(attached, attachmentInput)
		} else {
			expanded = attached
		}
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
	if prompt == "" {
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
	return s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.State()})
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

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{ID: s.ID, Workspace: s.Workspace, Running: s.running, Closed: s.closed, Model: s.Agent.Config.Model, Provider: s.Agent.Config.ActiveProvider, Messages: message.CloneAll(s.view), Steering: append([]string(nil), s.steering...), FollowUp: append([]string(nil), s.followUp...), Todos: s.Agent.GetTodos()}
}

func (s *Session) ClearQueue() {
	s.mu.Lock()
	s.steering = nil
	s.followUp = nil
	s.mu.Unlock()
	s.event(agent.Event{Type: "queue_update", SessionID: s.ID})
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

func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed && !s.running {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if err := s.Abort(ctx); err != nil {
		return err
	}
	s.runMu.Lock()
	settingsErr := s.persistSettings()
	s.runMu.Unlock()
	return errors.Join(settingsErr, s.event(agent.Event{Type: "session_end", SessionID: s.ID}), s.Agent.Dispose(ctx))
}
