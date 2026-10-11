package agent

import (
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
	"loop/pkg/domain/limits"
)

var mcpDebugLogging = os.Getenv("LOOP_MCP_DEBUG") == "1"

func mcpLog(format string, args ...interface{}) {
	if mcpDebugLogging {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

type mcpToolExecutor struct {
	agent    *Agent
	server   string
	client   *mcpClient
	toolName string
	def      Tool
}

func (m *mcpToolExecutor) Name() string     { return m.def.Function.Name }
func (m *mcpToolExecutor) Definition() Tool { return m.def }

func (m *mcpToolExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	client, err := m.resolveClient()
	if err != nil {
		return "", err
	}
	return client.callTool(m.toolName, arguments)
}

// resolveClient connects the owning server on demand when it was deferred at
// startup. A deferred server has no connection until its first tool call, so the
// call itself pays for the connect instead of every startup paying for every
// server.
func (m *mcpToolExecutor) resolveClient() (*mcpClient, error) {
	if m.client != nil {
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

// StartMCPServers connects configured servers. Servers marked alwaysLoad are awaited
// because their tools must be present for the first prompt. Servers without it are
// recorded as deferred and not started: first paint must not wait for a child process
// that may never be used. A deferred server is connected on first use, either through
// the load tool or through the registry hook when one of its tools is called.
// Eager servers connect through a bounded pool, so N servers no longer cost N
// sequential boot times.
func (a *Agent) StartMCPServers(configs map[string]config.MCPServerConfig) error {
	if a == nil {
		return fmt.Errorf("nil agent")
	}

	a.McpClientsMu.Lock()
	if a.McpClients == nil {
		a.McpClients = make(map[string]*mcpClient)
	}
	if a.McpStartErrors == nil {
		a.McpStartErrors = make(map[string]error)
	}
	if a.mcpPending == nil {
		a.mcpPending = make(map[string]config.MCPServerConfig)
	}
	a.mcpGeneration++
	generation := a.mcpGeneration

	var eager []string
	for name, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		if cfg.AlwaysLoad {
			eager = append(eager, name)
		} else {
			a.mcpPending[name] = cfg
		}
	}
	a.McpClientsMu.Unlock()

	sort.Strings(eager)

	workers := limits.MCPServerWorkers()
	if workers > len(eager) {
		workers = len(eager)
	}
	if workers < 1 {
		workers = 1
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, name := range eager {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a.connectServer(name, configs[name], generation)
		}(name)
	}
	wg.Wait()

	return nil
}

// LoadMCPServer connects one deferred server and reports what became available. It
// is the explicit entry point for on-demand loading, and it is also what a tool call
// through ResolveUnknownTool uses.
func (a *Agent) LoadMCPServer(name string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}

	a.McpClientsMu.Lock()
	if a.Config == nil {
		a.McpClientsMu.Unlock()
		return "", fmt.Errorf("no config")
	}
	cfg, exists := a.Config.MCPServers[name]
	generation := a.mcpGeneration
	a.McpClientsMu.Unlock()

	if !exists {
		return "", fmt.Errorf("MCP server '%s' is not configured", name)
	}
	if cfg.Disabled {
		return "", fmt.Errorf("MCP server '%s' is disabled", name)
	}

	client, err := a.connectServer(name, cfg, generation)
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("MCP server '%s' is not connected", name)
	}

	prefix := fmt.Sprintf("mcp__%s__", name)
	var loaded []string
	for toolName := range a.Registry.GetAllExecutors() {
		if strings.HasPrefix(toolName, prefix) {
			loaded = append(loaded, toolName)
		}
	}
	sort.Strings(loaded)

	if len(loaded) == 0 {
		return fmt.Sprintf("connected MCP server '%s' (no tools reported)", name), nil
	}
	return fmt.Sprintf("connected MCP server '%s': %s", name, strings.Join(loaded, ", ")), nil
}

// DeferredMCPServers lists configured servers that were not connected at startup,
// in stable order.
func (a *Agent) DeferredMCPServers() []string {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	var names []string
	for name := range a.mcpPending {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// connectServer boots one server and registers its tools. It is safe to call from
// the pool, from a deferred goroutine, and from a lazy tool call.
func (a *Agent) connectServer(name string, cfg config.MCPServerConfig, generation int) (*mcpClient, error) {
	if a == nil {
		return nil, fmt.Errorf("nil agent")
	}

	a.McpClientsMu.Lock()
	if a.McpClients == nil {
		a.McpClients = make(map[string]*mcpClient)
	}
	if a.McpStartErrors == nil {
		a.McpStartErrors = make(map[string]error)
	}
	// A connect clears the pending entry, so the deferred list reflects what is
	// actually still unconnected rather than what was once deferred.
	delete(a.mcpPending, name)
	if existing, ok := a.McpClients[name]; ok && existing != nil && existing.initialized {
		a.McpClientsMu.Unlock()
		return existing, nil
	}
	a.McpClientsMu.Unlock()

	client := a.newMCPClient(name, cfg)

	if err := client.start(); err != nil {
		a.recordMCPError(name, err)
		return nil, err
	}

	a.McpClientsMu.Lock()
	// A restart (or StopMCPServers) may have happened while this connect was in
	// flight; the generation changed, so this client belongs to a superseded
	// config and must not leak into the live map.
	if a.mcpGeneration != generation {
		a.McpClientsMu.Unlock()
		client.close()
		return nil, fmt.Errorf("MCP server '%s' connection superseded by a config reload", name)
	}
	a.McpClients[name] = client
	a.McpClientsMu.Unlock()
	mcpLog("Started MCP server '%s'", name)

	tools, err := client.listTools()
	if err != nil {
		mcpLog("Failed to list tools for MCP server '%s': %v", name, err)
		return client, nil
	}

	for _, mcpTool := range tools {
		prefixedName := fmt.Sprintf("mcp__%s__%s", name, mcpTool.Name)

		var jsonSchema JSONSchema
		if schemaBytes, err := json.Marshal(mcpTool.InputSchema); err == nil {
			_ = json.Unmarshal(schemaBytes, &jsonSchema)
		}

		a.Registry.Register(&mcpToolExecutor{
			agent:    a,
			server:   name,
			client:   client,
			toolName: mcpTool.Name,
			def: Tool{
				Type: "function",
				Function: FunctionDefinition{
					Name:        prefixedName,
					Description: fmt.Sprintf("[%s] %s", name, mcpTool.Description),
					Parameters:  jsonSchema,
				},
			},
		})
	}

	return client, nil
}

// ResolveUnknownTool resolves an MCP tool whose server was deferred at startup. The
// name is mcp__<server>__<tool>; the server is connected on demand, its tools are
// registered, and the requested executor is returned.
func (a *Agent) ResolveUnknownTool(name string) (tool.ToolExecutor, bool) {
	parts := strings.SplitN(name, "__", 3)
	if len(parts) != 3 || parts[0] != "mcp" {
		return nil, false
	}
	serverName := parts[1]

	client, err := a.connectMCPServer(serverName)
	if err != nil || client == nil {
		return nil, false
	}

	// connectMCPServer registered the tools; do not re-enter the registry through
	// Execute while its lock is held by the caller.
	executor, exists := a.Registry.Lookup(name)
	return executor, exists
}

// connectMCPServer is the lazy path used by tool executors: it connects using the
// generation the executor was registered under.
func (a *Agent) connectMCPServer(name string) (*mcpClient, error) {
	a.McpClientsMu.Lock()
	if a.Config == nil {
		a.McpClientsMu.Unlock()
		return nil, fmt.Errorf("no config")
	}
	cfg, exists := a.Config.MCPServers[name]
	generation := a.mcpGeneration
	a.McpClientsMu.Unlock()

	if !exists {
		return nil, fmt.Errorf("MCP server '%s' is no longer configured", name)
	}
	if cfg.Disabled {
		return nil, fmt.Errorf("MCP server '%s' is disabled", name)
	}
	return a.connectServer(name, cfg, generation)
}

func (a *Agent) newMCPClient(name string, cfg config.MCPServerConfig) *mcpClient {
	connectTimeout := limits.MCPConnectTimeout()

	// Timeout bounds a whole request; ResponseHeaderTimeout bounds only the connect
	// phase. They are different budgets: a tool call may legitimately take longer than
	// the time it should take for a server to start answering.
	transport := &http.Transport{ResponseHeaderTimeout: connectTimeout}
	if a.Config != nil && (a.Config.CertFile != "" || a.Config.SkipVerify) {
		if tlsConfig, err := config.GetTLSConfig(a.Config); err == nil && tlsConfig != nil {
			transport.TLSClientConfig = tlsConfig
		}
	}

	mcpHTTPClient := &http.Client{
		Timeout:   limits.MCPRequestTimeout(),
		Transport: transport,
	}
	if jar, err := cookiejar.New(nil); err == nil {
		mcpHTTPClient.Jar = jar
	}

	return &mcpClient{
		name:           name,
		config:         cfg,
		requests:       make(map[int64]chan string),
		earlyResponses: make(map[int64]string),
		nextID:         1,
		client:         mcpHTTPClient,
		connectTimeout: connectTimeout,
	}
}

func (a *Agent) recordMCPError(name string, err error) {
	a.McpClientsMu.Lock()
	if a.McpStartErrors == nil {
		a.McpStartErrors = make(map[string]error)
	}
	a.McpStartErrors[name] = err
	a.McpClientsMu.Unlock()
}

func (a *Agent) StopMCPServers() {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	// Bump the generation so in-flight connects from the previous config do not
	// register tools or land in the map after this point.
	a.mcpGeneration++

	for name, client := range a.McpClients {
		if client != nil {
			client.close()
			mcpLog("Stopped MCP server '%s'", name)
		}
	}
	a.McpClients = make(map[string]*mcpClient)
	a.Registry.UnregisterPrefix("mcp__")
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
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	status := make(map[string]string)
	for name, client := range a.McpClients {
		if client == nil {
			continue
		}
		if client.initialized {
			if client.isStdio {
				status[name] = fmt.Sprintf("Connected (stdio: %s)", strings.TrimSpace(client.config.Command+" "+strings.Join(client.config.Args, " ")))
			} else {
				status[name] = fmt.Sprintf("Connected (URL: %s)", client.config.URL)
			}
		} else {
			status[name] = "Initializing"
		}
	}
	return status
}
