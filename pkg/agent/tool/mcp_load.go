package tool

import (
	"fmt"
	"strings"
)

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
			"name": StringProp(FormatParamDescription("load_mcp", "name", "The configured MCP server name to connect. Optional: empty lists the deferred servers.")),
		},
		"name",
	)
}

func (t *loadMCPTool) Execute(ctx AgentContext, arguments string) (string, error) {
	loader, ok := ctx.(MCPLoader)
	if !ok {
		return "", fmt.Errorf("agent does not support on-demand MCP loading")
	}

	var raw string
	var args struct {
		Name       string `json:"name"`
		ServerName string `json:"server"`
	}
	if trimmed := strings.TrimSpace(arguments); trimmed != "" && trimmed != "{}" {
		if err := ParseToolArguments(arguments, &args, func(s string) { raw = s }); err != nil {
			return "", fmt.Errorf("JSON validation failed: %w", err)
		}
	}
	name := strings.TrimSpace(FirstNonEmpty(raw, args.Name, args.ServerName))

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