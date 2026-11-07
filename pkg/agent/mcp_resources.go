package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"loop/pkg/config"
	"loop/pkg/resources"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/domain/message"
)

type mcpResult struct {
	Content []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Data     string          `json:"data"`
		MimeType string          `json:"mimeType"`
		Resource json.RawMessage `json:"resource"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
	IsError    bool            `json:"isError"`
}

func (m *mcpToolExecutor) ExecuteResult(ctx tool.AgentContext, arguments string) (tool.ToolResult, error) {
	client, err := m.resolveClient()
	if err != nil {
		return tool.ToolResult{}, err
	}
	parent := context.Background()
	if ctx != nil {
		parent = ctx.Context()
	}
	return client.callToolResult(parent, m.toolName, arguments)
}

func (client *mcpClient) callToolResult(parent context.Context, name, arguments string) (tool.ToolResult, error) {
	if !client.initialized {
		return tool.ToolResult{}, fmt.Errorf("client not initialized")
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(arguments), &params); err != nil {
		return tool.ToolResult{}, err
	}
	raw, err := client.resourceRequest(parent, "tools/call", map[string]any{"name": name, "arguments": params})
	if err != nil {
		return tool.ToolResult{}, err
	}
	var result mcpResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return tool.ToolResult{}, err
	}
	output := tool.ToolResult{Structured: result.Structured, IsError: result.IsError}
	var text []string
	for _, part := range result.Content {
		switch part.Type {
		case "text":
			text = append(text, part.Text)
		case "resource":
			if len(part.Resource) > 0 {
				text = append(text, string(part.Resource))
			}
		case "image":
			output.Content = append(output.Content, message.ContentBlock{Type: "image", Data: part.Data, MimeType: part.MimeType})
		}
	}
	output.Output = strings.Join(text, "\n")
	if output.IsError {
		if output.Output != "" {
			return output, fmt.Errorf("%s", output.Output)
		}
		return output, fmt.Errorf("MCP tool %s failed", name)
	}
	return output, nil
}

type mcpResourceTool struct {
	agent  *Agent
	name   string
	method string
}

func (t *mcpResourceTool) Name() string          { return t.name }
func (t *mcpResourceTool) ExecutionMode() string { return "parallel" }
func (t *mcpResourceTool) Definition() tool.Tool {
	description := "Read one MCP resource by URI. Non-image binary resources are saved as local artifacts."
	if t.name == "list_mcp_resources" {
		description = "List resources available from configured MCP servers."
	}
	if t.name == "list_mcp_resource_templates" {
		description = "List parameterized resource templates from configured MCP servers."
	}
	return tool.NewFunctionTool(t.name, description, map[string]tool.SchemaProp{"server": tool.StringProp("Configured server name"), "uri": tool.StringProp("Resource URI"), "cursor": tool.StringProp("Pagination cursor")})
}
func (t *mcpResourceTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	result, err := t.ExecuteResult(ctx, arguments)
	return result.Output, err
}
func (t *mcpResourceTool) ExecuteResult(ctx tool.AgentContext, arguments string) (tool.ToolResult, error) {
	var args struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return tool.ToolResult{}, err
	}
	parent := context.Background()
	if ctx != nil {
		parent = ctx.Context()
	}
	var names []string
	if args.Server != "" {
		names = []string{args.Server}
	} else {
		for name, cfg := range t.agent.Config.MCPServers {
			if cfg.Enabled {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	if t.method == "resources/read" && (len(names) != 1 || args.URI == "") {
		return tool.ToolResult{}, fmt.Errorf("read_mcp_resource requires server and uri")
	}
	results := make(map[string]json.RawMessage)
	var blocks []message.ContentBlock
	for _, name := range names {
		client, err := t.agent.connectMCPServer(name)
		if err != nil {
			return tool.ToolResult{}, err
		}
		params := map[string]string{}
		if args.Cursor != "" {
			params["cursor"] = args.Cursor
		}
		if args.URI != "" {
			params["uri"] = args.URI
		}
		raw, err := client.resourceRequest(parent, t.method, params)
		if err != nil {
			return tool.ToolResult{}, fmt.Errorf("MCP %s: %w", name, err)
		}
		results[name] = raw
		if t.method == "resources/read" {
			var response struct {
				Contents []struct {
					Text     string `json:"text"`
					Blob     string `json:"blob"`
					MimeType string `json:"mimeType"`
					URI      string `json:"uri"`
				} `json:"contents"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				return tool.ToolResult{}, err
			}
			var contents []map[string]any
			for _, content := range response.Contents {
				item := map[string]any{"uri": content.URI, "mimeType": content.MimeType}
				if content.Text != "" {
					item["text"] = content.Text
				}
				if content.Blob != "" {
					data, err := base64.StdEncoding.DecodeString(content.Blob)
					if err != nil {
						return tool.ToolResult{}, fmt.Errorf("invalid MCP resource encoding")
					}
					if strings.HasPrefix(content.MimeType, "image/") {
						blocks = append(blocks, message.ContentBlock{Type: "image", Data: content.Blob, MimeType: content.MimeType})
						item["image"] = true
					} else {
						hash := sha256.Sum256(data)
						path := filepath.Join(resources.AgentDirectory(t.agent.ConfigPath), "artifacts", "mcp", fmt.Sprintf("%x", hash[:]))
						if err := config.WritePrivateFile(path, data); err != nil {
							return tool.ToolResult{}, err
						}
						item["artifact"] = path
						item["bytes"] = len(data)
					}
				}
				contents = append(contents, item)
			}
			encoded, err := json.Marshal(map[string]any{"contents": contents})
			if err != nil {
				return tool.ToolResult{}, err
			}
			results[name] = encoded
		}
	}
	encoded, err := json.Marshal(results)
	return tool.ToolResult{Output: string(encoded), Structured: encoded, Content: blocks}, err
}

func (a *Agent) registerResourceTools() {
	for _, entry := range []struct{ name, method string }{{"list_mcp_resources", "resources/list"}, {"list_mcp_resource_templates", "resources/templates/list"}, {"read_mcp_resource", "resources/read"}} {
		a.Registry.Register(&mcpResourceTool{agent: a, name: entry.name, method: entry.method})
	}
}
