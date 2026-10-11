package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MCPLoader is implemented by agents that connect MCP servers on demand. A server
// configured without alwaysLoad is not connected at startup, so its tools do not
// exist until something connects it; this is the explicit entry point for that.
// The agent satisfies it, so the tool needs no direct dependency on the agent
// package.
type MCPLoader interface {
	LoadMCPServer(name string) (string, error)
	DeferredMCPServers() []string
}

type loadMCPTool struct{}

func NewLoadMCPTool() ToolExecutor {
	return &loadMCPTool{}
}

func (t *loadMCPTool) Name() string { return "load_mcp" }

func (t *loadMCPTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Connect a deferred MCP server and make its tools available")
}

func (t *loadMCPTool) Definition() Tool {
	return NewFunctionTool(
		"load_mcp",
		FormatToolDescription("load_mcp", "Connect an MCP server that was configured without alwaysLoad and register its tools. Omit the name to list the servers that are still deferred."),
		map[string]SchemaProp{
			"name": {
				Type:        "string",
				Description: FormatParamDescription("load_mcp", "name", "The configured MCP server name to connect. Optional: empty lists the deferred servers."),
			},
		},
		"name",
	)
}

func (t *loadMCPTool) Execute(ctx AgentContext, arguments string) (string, error) {
	loader, ok := ctx.(MCPLoader)
	if !ok {
		return "", fmt.Errorf("agent does not support on-demand MCP loading")
	}

	name := strings.TrimSpace(arguments)
	if strings.HasPrefix(name, "{") {
		var args struct {
			Name       string `json:"name"`
			ServerName string `json:"server"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Name != "" {
				name = args.Name
			} else if args.ServerName != "" {
				name = args.ServerName
			}
		} else {
			return "", fmt.Errorf("JSON validation failed: %w", err)
		}
	} else if strings.HasPrefix(name, "\"") && strings.HasSuffix(name, "\"") && len(name) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(name), &unquoted); err == nil {
			name = unquoted
		}
	}
	name = strings.TrimSpace(name)

	deferred := loader.DeferredMCPServers()

	if name == "" {
		if len(deferred) == 0 {
			return "no MCP servers are deferred: every configured server is connected", nil
		}
		return fmt.Sprintf("deferred MCP servers: %s", strings.Join(deferred, ", ")), nil
	}

	summary, err := loader.LoadMCPServer(name)
	if err != nil {
		return "", err
	}
	if summary == "" {
		summary = fmt.Sprintf("connected MCP server '%s'", name)
	}
	return summary, nil
}
