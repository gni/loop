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
	client, err := m.resolveClient()
	if err != nil {
		return "", err
	}
	return client.callTool(m.toolName, arguments)
}

func (m *mcpToolExecutor) resolveClient() (*mcpClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

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
			_, _ = a.connectServer(name, configs[name], generation)
		}(name)
	}
	wg.Wait()

	return nil
}

func (a *Agent) LoadMCPServer(name string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("nil agent")
	}

	client, err := a.connectMCPServer(name)
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

func (a *Agent) DeferredMCPServers() []string {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

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

	a.McpClientsMu.Lock()
	if a.McpClients == nil {
		a.McpClients = make(map[string]*mcpClient)
	}
	if a.McpStartErrors == nil {
		a.McpStartErrors = make(map[string]error)
	}
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
	if len(tools) > 0 {
		a.markSystemPromptDirty()
	}

	return client, nil
}

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

	return a.Registry.Lookup(name)
}

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
		return nil, fmt.Errorf("MCP server '%s' is not configured", name)
	}
	if cfg.Disabled {
		return nil, fmt.Errorf("MCP server '%s' is disabled", name)
	}
	return a.connectServer(name, cfg, generation)
}

func (a *Agent) newMCPClient(name string, cfg config.MCPServerConfig) *mcpClient {
	connectTimeout := limits.MCPConnectTimeout()

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
	a.mcpGeneration++

	for name, client := range a.McpClients {
		if client != nil {
			client.close()
			mcpLog("Stopped MCP server '%s'", name)
		}
	}
	a.McpClients = make(map[string]*mcpClient)
	a.McpClientsMu.Unlock()

	a.Registry.UnregisterPrefix("mcp__")
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
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	status := make(map[string]string, len(a.McpClients))
	for name, client := range a.McpClients {
		if client == nil {
			continue
		}
		if !client.initialized {
			status[name] = "Initializing"
			continue
		}
		if client.isStdio {
			status[name] = fmt.Sprintf("Connected (stdio: %s)", strings.TrimSpace(client.config.Command+" "+strings.Join(client.config.Args, " ")))
		} else {
			status[name] = fmt.Sprintf("Connected (URL: %s)", client.config.URL)
		}
	}
	return status
}