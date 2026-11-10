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
	Store         domainsession.ContextStore
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
	Store     domainsession.ContextStore
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
	loaded, err := resources.LoadPackagesContext(ctx, packageDirs...)
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
		store = store.ScopedStore(workspace)
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
			return nil, fmt.Errorf("load session %s: %w", id, readErr)
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
