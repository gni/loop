package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
		URI      string          `json:"uri"`
		Name     string          `json:"name"`
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

func (client *mcpClient) callToolResult(ctx context.Context, name, arguments string) (tool.ToolResult, error) {
	if !client.initialized {
		return tool.ToolResult{}, fmt.Errorf("client not initialized")
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &params); err != nil {
		return tool.ToolResult{}, err
	}
	if params == nil {
		return tool.ToolResult{}, fmt.Errorf("MCP tool arguments must be a JSON object")
	}
	var result mcpResult
	if err := client.requestResult(ctx, "tools/call", map[string]any{"name": name, "arguments": params}, &result); err != nil {
		return tool.ToolResult{}, err
	}

	output := tool.ToolResult{IsError: result.IsError}
	var text []string
	for _, part := range result.Content {
		switch part.Type {
		case "text":
			if strings.TrimSpace(part.Text) != "" {
				text = append(text, part.Text)
			}
		case "resource":
			if len(part.Resource) > 0 {
				text = append(text, string(part.Resource))
			}
		case "resource_link":
			text = append(text, fmt.Sprintf("MCP resource %q at %q (server %q)", part.Name, part.URI, client.name))
		case "image":
			output.Content = append(output.Content, message.ContentBlock{Type: "image", Data: part.Data, MimeType: part.MimeType})
		default:
			text = append(text, fmt.Sprintf("[Unsupported MCP content type %q]", part.Type))
		}
	}
	if len(result.Structured) > 0 && !bytes.Equal(result.Structured, []byte("null")) {
		var compact bytes.Buffer
		// RawMessage was validated while decoding the RPC response.
		_ = json.Compact(&compact, result.Structured)
		output.Structured = compact.Bytes()
		structured := compact.String()
		if !hasMCPStructuredText(text, structured) {
			text = append(text, structured)
		}
	}
	output.Output = strings.Join(text, "\n")
	if output.IsError {
		if output.Output != "" {
			return output, errors.New(output.Output)
		}
		return output, fmt.Errorf("MCP tool %s failed", name)
	}
	if output.Output == "" && len(output.Content) > 0 {
		output.Output = "(see attached image)"
	}
	return output, nil
}

// The compatibility text can represent the same object with different key
// order or whitespace. Keep one copy in the model-facing result.
func hasMCPStructuredText(text []string, structured string) bool {
	var expected map[string]any
	for _, part := range text {
		part = strings.TrimSpace(part)
		if part == structured {
			return true
		}
		if !strings.HasPrefix(part, "{") || !strings.HasPrefix(structured, "{") {
			continue
		}
		var candidate map[string]any
		decoder := json.NewDecoder(strings.NewReader(part))
		decoder.UseNumber()
		if err := decoder.Decode(&candidate); err != nil || strings.TrimSpace(part[decoder.InputOffset():]) != "" {
			continue
		}
		if expected == nil {
			decoder = json.NewDecoder(strings.NewReader(structured))
			decoder.UseNumber()
			if err := decoder.Decode(&expected); err != nil {
				return false
			}
		}
		if reflect.DeepEqual(candidate, expected) {
			return true
		}
	}
	return false
}
