package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"sync"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	transporthttp "loop/pkg/transport/http"
)

var mcpDebugLogging = os.Getenv("LOOP_MCP_DEBUG") == "1"

// mcpState owns connection generations, in-flight attempts and published clients.
// Configuration and transport replacement happen while the session is idle.
type mcpState struct {
	MCPClients     map[string]*mcpClient
	MCPClientsMu   sync.Mutex
	MCPStartErrors map[string]error
	mcpGeneration  int
	mcpPending     map[string]config.MCPServerConfig
	mcpConnecting  map[string]*mcpConnectAttempt
}

func mcpLog(format string, args ...interface{}) {
	if mcpDebugLogging {
		fmt.Fprint(os.Stderr, transporthttp.RedactDiagnostic(fmt.Sprintf(format, args...)))
	}
}

type mcpConnectAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	client *mcpClient
	err    error
}

func mcpToolPrefix(server string) string {
	if len(server) > 28 || strings.Contains(server, "__") || !tool.IsSafeIdentifier(server) {
		sum := sha256.Sum256([]byte(server))
		server = "s_" + hex.EncodeToString(sum[:12])
	}
	return "mcp__" + server + "__"
}

// MCPToolServer resolves ownership from the executor, or its stable namespace
// before a deferred server has connected. Ambiguous namespaces are rejected.
func (a *Agent) MCPToolServer(name string) (string, bool) {
	if executor, ok := a.Registry.Lookup(name); ok {
		if owner, ok := executor.(*mcpToolExecutor); ok {
			return owner.server, true
		}
		return "", false
	}
	if a.Config == nil {
		return "", false
	}
	owner := ""
	for server := range a.Config.MCPServers {
		if strings.HasPrefix(name, mcpToolPrefix(server)) {
			if owner != "" {
				return "", false
			}
			owner = server
		}
	}
	return owner, owner != ""
}

type mcpToolExecutor struct {
	mu       sync.Mutex
	agent    *Agent
	server   string
	client   *mcpClient
	toolName string
	def      Tool
}

func (m *mcpToolExecutor) Name() string     { return m.def.Function.Name }
func (m *mcpToolExecutor) Definition() Tool { return m.def }

func (m *mcpToolExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	result, err := m.ExecuteResult(ctx, arguments)
	return result.Output, err
}

func (m *mcpToolExecutor) resolveClient() (*mcpClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client != nil && m.client.healthy() {
		return m.client, nil
	}
	if m.agent == nil {
		return nil, fmt.Errorf("MCP server '%s' is not connected", m.server)
	}
	client, err := m.agent.connectMCPServer(m.server)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("MCP server '%s' is not connected", m.server)
	}
	m.client = client
	return client, nil
}

func (a *Agent) StartMCPServers(configs map[string]config.MCPServerConfig) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}
	if a.Config == nil || !a.Config.MCP {
		return nil
	}

	if len(configs) > 0 {
		a.registerResourceTools()
	}
	a.MCPClientsMu.Lock()
	if a.MCPClients == nil {
		a.MCPClients = make(map[string]*mcpClient)
	}
	if a.MCPStartErrors == nil {
		a.MCPStartErrors = make(map[string]error)
	}
	if a.mcpPending == nil {
		a.mcpPending = make(map[string]config.MCPServerConfig)
	}
	a.mcpGeneration++
	generation := a.mcpGeneration

	var eager []string
	for name, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		if cfg.AlwaysLoad {
			eager = append(eager, name)
		} else {
			a.mcpPending[name] = cfg
		}
	}
	a.MCPClientsMu.Unlock()

	sort.Strings(eager)

	workers := a.GetLimits().MCPServerWorkers
	if workers > len(eager) {
		workers = len(eager)
	}
	if workers < 1 {
		workers = 1
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var failuresMu sync.Mutex
	var failures []string
	for _, name := range eager {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := a.connectServer(name, configs[name], generation); err != nil {
				failuresMu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				failuresMu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("MCP startup failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (a *Agent) LoadMCPServer(name string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}
	if a.Config == nil || !a.Config.MCP {
		return "", fmt.Errorf("MCP is disabled; set mcp=true")
	}

	client, err := a.connectMCPServer(name)
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("MCP server '%s' is not connected", name)
	}

	var loaded []string
	for toolName, executor := range a.Registry.GetAllExecutors() {
		if owner, ok := executor.(*mcpToolExecutor); ok && owner.server == name {
			loaded = append(loaded, toolName)
		}
	}
	sort.Strings(loaded)

	if len(loaded) == 0 {
		return fmt.Sprintf("connected MCP server '%s' (no tools reported)", name), nil
	}
	return fmt.Sprintf("connected MCP server '%s': %s", name, strings.Join(loaded, ", ")), nil
}

func (a *Agent) DeferredMCPServers() []string {
	a.MCPClientsMu.Lock()
	defer a.MCPClientsMu.Unlock()

	names := make([]string, 0, len(a.mcpPending))
	for name := range a.mcpPending {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *Agent) connectServer(name string, cfg config.MCPServerConfig, generation int) (*mcpClient, error) {
	if a == nil {
		return nil, fmt.Errorf("nil agent")
	}

	a.MCPClientsMu.Lock()
	if a.mcpGeneration != generation {
		a.MCPClientsMu.Unlock()
		return nil, fmt.Errorf("MCP server '%s' connection superseded by a config reload", name)
	}
	if existing := a.MCPClients[name]; existing != nil && existing.initialized && existing.healthy() {
		a.MCPClientsMu.Unlock()
		return existing, nil
	}
	if attempt := a.mcpConnecting[name]; attempt != nil {
		a.MCPClientsMu.Unlock()
		<-attempt.done
		a.MCPClientsMu.Lock()
		stale := a.mcpGeneration != generation
		a.MCPClientsMu.Unlock()
		if stale {
			return nil, fmt.Errorf("MCP server '%s' connection superseded by a config reload", name)
		}
		return attempt.client, attempt.err
	}
	if a.mcpConnecting == nil {
		a.mcpConnecting = make(map[string]*mcpConnectAttempt)
	}
	client := a.newMCPClient(name, cfg)
	attempt := &mcpConnectAttempt{done: make(chan struct{}), cancel: client.cancel}
	a.mcpConnecting[name] = attempt
	a.MCPClientsMu.Unlock()

	client, err := a.startAndRegisterMCPServer(client, generation)
	err = transporthttp.RedactError(err)
	a.MCPClientsMu.Lock()
	if a.mcpGeneration != generation {
		client, err = nil, fmt.Errorf("MCP server '%s' connection superseded by a config reload", name)
	}
	attempt.client, attempt.err = client, err
	if err != nil && a.mcpGeneration == generation {
		if a.MCPStartErrors == nil {
			a.MCPStartErrors = make(map[string]error)
		}
		a.MCPStartErrors[name] = err
	}
	if a.mcpConnecting[name] == attempt {
		delete(a.mcpConnecting, name)
	}
	close(attempt.done)
	a.MCPClientsMu.Unlock()
	return client, err
}

func (a *Agent) startAndRegisterMCPServer(client *mcpClient, generation int) (*mcpClient, error) {
	name, cfg := client.name, client.config
	published := false
	defer func() {
		if !published {
			client.close()
		}
	}()
	if strings.TrimSpace(cfg.Command) != "" && !a.IsShellExecutionAllowed() {
		return nil, fmt.Errorf("local MCP executables require a trusted workspace")
	}
	if err := client.start(); err != nil {
		return nil, err
	}

	tools, err := client.listTools()
	if err != nil {
		err = fmt.Errorf("MCP server '%s' failed to list tools: %w", name, err)
		return nil, err
	}

	executors := make([]tool.ToolExecutor, 0, len(tools))
	names := make(map[string]bool, len(tools))
	for _, mcpTool := range tools {
		prefixedName := mcpToolPrefix(name) + mcpTool.Name
		if len(prefixedName) > 64 || !tool.IsSafeIdentifier(prefixedName) || strings.Contains(mcpTool.Name, "__") {
			sum := sha256.Sum256([]byte(mcpTool.Name))
			prefixedName = mcpToolPrefix(name) + "tool_" + hex.EncodeToString(sum[:12])
		}
		if names[prefixedName] {
			return nil, fmt.Errorf("MCP server %q reported duplicate tool %q", name, mcpTool.Name)
		}
		names[prefixedName] = true
		var jsonSchema JSONSchema
		if schemaBytes, marshalErr := json.Marshal(mcpTool.InputSchema); marshalErr == nil {
			if err := json.Unmarshal(schemaBytes, &jsonSchema); err != nil {
				mcpLog("Skipping MCP tool %q with unsupported schema: %v\n", mcpTool.Name, err)
				continue
			}
		} else {
			mcpLog("Skipping MCP tool %q with invalid schema: %v\n", mcpTool.Name, marshalErr)
			continue
		}
		executors = append(executors, &mcpToolExecutor{
			agent: a, server: name, client: client, toolName: mcpTool.Name,
			def: Tool{Type: "function", Function: FunctionDefinition{
				Name: prefixedName, Description: fmt.Sprintf("[%s] %s", name, mcpTool.Description),
				Parameters: jsonSchema,
			}},
		})
	}

	a.MCPClientsMu.Lock()
	if a.mcpGeneration != generation {
		a.MCPClientsMu.Unlock()
		return nil, fmt.Errorf("MCP server '%s' connection superseded by a config reload", name)
	}
	if a.MCPClients == nil {
		a.MCPClients = make(map[string]*mcpClient)
	}
	previous := a.MCPClients[name]
	registered := a.Registry.GetAllExecutors()
	for _, executor := range executors {
		if existing := registered[executor.Name()]; existing != nil {
			if owner, ok := existing.(*mcpToolExecutor); !ok || owner.server != name {
				a.MCPClientsMu.Unlock()
				return nil, fmt.Errorf("MCP tool name collision for %q", executor.Name())
			}
		}
	}
	for toolName, executor := range registered {
		if owner, ok := executor.(*mcpToolExecutor); ok && owner.server == name {
			a.Registry.Unregister(toolName)
		}
	}
	a.MCPClients[name] = client
	delete(a.mcpPending, name)
	delete(a.MCPStartErrors, name)
	for _, executor := range executors {
		a.Registry.Register(executor)
		if cfg.Exposure == "search" {
			a.Registry.Hide(executor.Name())
		}
	}
	a.MCPClientsMu.Unlock()
	published = true
	if previous != nil && previous != client {
		previous.close()
	}
	a.markSystemPromptDirty()
	mcpLog("Started MCP server '%s' with %d tools\n", name, len(executors))
	return client, nil
}

func (a *Agent) ResolveUnknownTool(name string) (tool.ToolExecutor, bool) {
	// Resolving an unknown name must never connect to an MCP server. The
	// model must invoke load_mcp and pass its approval gate first.
	if a == nil || a.Registry == nil {
		return nil, false
	}
	return a.Registry.Lookup(name)
}

func (a *Agent) connectMCPServer(name string) (*mcpClient, error) {
	a.MCPClientsMu.Lock()
	if a.Config == nil || !a.Config.MCP {
		a.MCPClientsMu.Unlock()
		return nil, fmt.Errorf("MCP is disabled; set mcp=true")
	}
	cfg, exists := a.Config.MCPServers[name]
	generation := a.mcpGeneration
	a.MCPClientsMu.Unlock()

	if !exists {
		return nil, fmt.Errorf("MCP server '%s' is not configured", name)
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("MCP server '%s' is disabled", name)
	}
	return a.connectServer(name, cfg, generation)
}

func (a *Agent) newMCPClient(name string, cfg config.MCPServerConfig) *mcpClient {
	connectTimeout := a.GetLimits().MCPConnectTimeout

	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
		transport = base.Clone()
	} else {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	transport.ResponseHeaderTimeout = connectTimeout
	if a.Config != nil && (a.Config.CAFile != "" || a.Config.CertFile != "" || a.Config.KeyFile != "" || !a.Config.TLSVerify) {
		if tlsConfig, err := config.GetTLSConfig(a.Config); err == nil && tlsConfig != nil {
			transport.TLSClientConfig = tlsConfig
		}
	}

	mcpHTTPClient := &http.Client{
		Timeout:   a.GetLimits().MCPRequestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && (req.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	if jar, err := cookiejar.New(nil); err == nil {
		mcpHTTPClient.Jar = jar
	}

	lifetime := a.Lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	ctx, cancel := context.WithCancel(lifetime)
	return &mcpClient{
		ctx:            ctx,
		cancel:         cancel,
		name:           name,
		config:         cfg,
		requests:       make(map[int64]chan string),
		nextID:         1,
		client:         mcpHTTPClient,
		connectTimeout: connectTimeout,
		timeout:        a.GetLimits().MCPRequestTimeout,
		resolveCredential: func(ctx context.Context) (string, error) {
			if a.Credentials == nil {
				return "", nil
			}
			return a.Credentials.Resolve(ctx, "mcp:"+name+":"+config.WorkspaceKey(cfg.URL), config.ProviderConfig{Endpoint: cfg.URL, OAuth: cfg.OAuth})
		},
	}
}

func (a *Agent) StopMCPServers() {
	a.MCPClientsMu.Lock()
	a.mcpGeneration++
	clients := make([]*mcpClient, 0, len(a.MCPClients))
	for _, client := range a.MCPClients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	attempts := make([]*mcpConnectAttempt, 0, len(a.mcpConnecting))
	for _, attempt := range a.mcpConnecting {
		attempts = append(attempts, attempt)
	}
	a.MCPClients = make(map[string]*mcpClient)
	a.mcpConnecting = make(map[string]*mcpConnectAttempt)
	a.mcpPending = make(map[string]config.MCPServerConfig)
	a.MCPStartErrors = make(map[string]error)
	a.Registry.UnregisterPrefix("mcp__")
	a.MCPClientsMu.Unlock()
	for _, attempt := range attempts {
		attempt.cancel()
	}

	// Network and process teardown must not hold the connection registry lock.
	workers := min(len(clients), max(1, a.GetLimits().MCPServerWorkers))
	jobs := make(chan *mcpClient)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for client := range jobs {
				client.close()
			}
		}()
	}
	for _, client := range clients {
		jobs <- client
	}
	close(jobs)
	wg.Wait()
	for _, attempt := range attempts {
		<-attempt.done
	}
	a.markSystemPromptDirty()
}

func (a *Agent) GetMCPTools() []Tool {
	var allTools []Tool
	for name, t := range a.Registry.GetAllExecutors() {
		if strings.HasPrefix(name, "mcp__") {
			allTools = append(allTools, t.Definition())
		}
	}

	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Function.Name < allTools[j].Function.Name
	})

	return allTools
}

func (a *Agent) GetMCPServersStatus() map[string]string {
	statuses := a.MCPServerStatuses()
	status := make(map[string]string, len(statuses))
	for name, snapshot := range statuses {
		switch snapshot.State {
		case "connected":
			status[name] = fmt.Sprintf("Connected (%d tools)", snapshot.Tools)
		case "connecting":
			status[name] = "Connecting"
		case "deferred":
			status[name] = "On demand"
		case "failed":
			status[name] = "Failed: " + snapshot.Error
		default:
			status[name] = "Not connected"
		}
	}
	return status
}
