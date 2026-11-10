package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/domain/message"
	"loop/pkg/resources"
)

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
	return tool.NewFunctionTool(t.name, description, map[string]tool.SchemaProp{
		"server": tool.StringProp("Configured server name. Select the server that returned the cursor when continuing a listing."),
		"uri":    tool.StringProp("Resource URI for read_mcp_resource"),
		"cursor": tool.StringProp("Pagination cursor returned by the selected server"),
	})
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
	if args.Cursor != "" && len(names) > 1 {
		return tool.ToolResult{}, fmt.Errorf("select the MCP server that returned this cursor")
	}
	params := map[string]string{}
	if args.Cursor != "" {
		params["cursor"] = args.Cursor
	}
	if args.URI != "" {
		params["uri"] = args.URI
	}
	results := make(map[string]json.RawMessage)
	var blocks []message.ContentBlock
	failures := 0
	for _, name := range names {
		if err := parent.Err(); err != nil {
			return tool.ToolResult{}, err
		}
		client, err := t.agent.connectMCPServer(name)
		if err == nil && client.capabilities != nil && client.capabilities.Resources == nil {
			if args.Server != "" || t.method == "resources/read" {
				return tool.ToolResult{}, fmt.Errorf("MCP server %q does not advertise resources", name)
			}
			continue
		}
		var raw json.RawMessage
		if err == nil {
			err = client.requestResult(parent, t.method, params, &raw)
		}
		if err != nil {
			if parent.Err() != nil {
				return tool.ToolResult{}, parent.Err()
			}
			if args.Server != "" || t.method == "resources/read" {
				return tool.ToolResult{}, fmt.Errorf("MCP %s: %w", name, err)
			}
			results[name], _ = json.Marshal(map[string]string{"error": err.Error()})
			failures++
			continue
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
	if err == nil && failures > 0 && failures == len(results) {
		err = fmt.Errorf("%s failed on every queried server", t.method)
	}
	return tool.ToolResult{Output: string(encoded), Structured: encoded, Content: blocks}, err
}

func (a *Agent) registerResourceTools() {
	for _, entry := range []struct{ name, method string }{{"list_mcp_resources", "resources/list"}, {"list_mcp_resource_templates", "resources/templates/list"}, {"read_mcp_resource", "resources/read"}} {
		a.Registry.Register(&mcpResourceTool{agent: a, name: entry.name, method: entry.method})
	}
}
