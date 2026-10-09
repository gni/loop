package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
)

var mcpDebugLogging = os.Getenv("LOOP_MCP_DEBUG") == "1"

func mcpLog(format string, args ...interface{}) {
	if mcpDebugLogging {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

type mcpToolExecutor struct {
	client   *mcpClient
	toolName string
	def      Tool
}

func (m *mcpToolExecutor) Name() string     { return m.def.Function.Name }
func (m *mcpToolExecutor) Definition() Tool { return m.def }
func (m *mcpToolExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	return m.client.callTool(m.toolName, arguments)
}

func (a *Agent) StartMCPServers(configs map[string]config.MCPServerConfig) error {
	a.McpClientsMu.Lock()
	if a.McpClients == nil {
		a.McpClients = make(map[string]*mcpClient)
	}
	if a.McpStartErrors == nil {
		a.McpStartErrors = make(map[string]error)
	}
	a.McpClientsMu.Unlock()

	for name, cfg := range configs {
		if cfg.Disabled {
			continue
		}

		mcpHTTPClient := &http.Client{
			Timeout: 30 * time.Second,
		}

		if jar, err := cookiejar.New(nil); err == nil {
			mcpHTTPClient.Jar = jar
		}

		if a.Config != nil && (a.Config.CertFile != "" || a.Config.SkipVerify) {
			tlsConfig, err := config.GetTLSConfig(a.Config)
			if err == nil && tlsConfig != nil {
				transport := &http.Transport{
					TLSClientConfig: tlsConfig,
				}
				mcpHTTPClient.Transport = transport
			}
		}

		client := &mcpClient{
			name:     name,
			config:   cfg,
			requests: make(map[int64]chan string),
			nextID:   1,
			client:   mcpHTTPClient,
		}

		err := client.start()
		if err != nil {
			a.McpClientsMu.Lock()
			a.McpStartErrors[name] = err
			a.McpClientsMu.Unlock()
			continue
		}

		a.McpClientsMu.Lock()
		a.McpClients[name] = client
		a.McpClientsMu.Unlock()
		mcpLog("Started MCP server '%s'\n", name)

		tools, err := client.listTools()
		if err != nil {
			mcpLog("Error: Failed to list tools for MCP server '%s': %v\n", name, err)
			continue
		}

		for _, mcpTool := range tools {
			prefixedName := fmt.Sprintf("mcp__%s__%s", name, mcpTool.Name)

			var jsonSchema JSONSchema
			schemaBytes, err := json.Marshal(mcpTool.InputSchema)
			if err == nil {
				_ = json.Unmarshal(schemaBytes, &jsonSchema)
			}

			a.Registry.Register(&mcpToolExecutor{
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
	}

	return nil
}

func (a *Agent) StopMCPServers() {
	a.McpClientsMu.Lock()
	defer a.McpClientsMu.Unlock()

	for name, client := range a.McpClients {
		client.close()
		mcpLog("Stopped MCP server '%s'\n", name)
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
		if client.initialized {
			status[name] = fmt.Sprintf("Connected (URL: %s)", client.config.URL)
		} else {
			status[name] = "Initializing"
		}
	}
	return status
}
