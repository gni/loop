package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"loop/pkg/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMCPHTTPRichResultsResourcesAndReconnect(t *testing.T) {
	var starts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Configured") != "yes" {
			t.Error("missing custom header")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			starts.Add(1)
			w.Header().Set("Mcp-Session-Id", "fixture-session")
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}, "resources": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo.remote", "description": "remote echo", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}, map[string]any{"type": "image", "mimeType": "image/png", "data": "aW1hZ2U="}}, "structuredContent": map[string]any{"ok": true}}
		case "resources/list":
			result = map[string]any{"resources": []any{map[string]any{"uri": "test://one", "name": "one"}}}
		case "resources/read":
			result = map[string]any{"contents": []any{map[string]any{"uri": "test://one", "text": "resource content", "mimeType": "text/plain"}}}
		default:
			http.Error(w, "unexpected method "+request.Method, 400)
			return
		}
		if request.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "fixture-session" {
			t.Error("missing MCP session identity")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.DisableLocalPlugins = true
	cfg.MCPServers = map[string]config.MCPServerConfig{"fixture": {URL: server.URL, Headers: map[string]string{"X-Configured": "yes"}, AlwaysLoad: true}}
	a := NewAgentAt(cfg, "", server.Client(), t.TempDir())
	defer a.Dispose(context.Background())
	if err := a.StartMCPServers(cfg.MCPServers); err != nil {
		t.Fatal(err)
	}
	definitions := a.GetMCPTools()
	if len(definitions) != 1 || !strings.HasPrefix(definitions[0].Function.Name, "mcp__fixture__tool_") {
		t.Fatalf("invalid remote name was lost: %+v", definitions)
	}
	result, err := a.Registry.ExecuteResult(a, definitions[0].Function.Name, `{}`)
	if err != nil || result.Output != "hello" || len(result.Content) != 1 || string(result.Structured) != `{"ok":true}` {
		t.Fatalf("rich result lost: %+v %v", result, err)
	}
	resources, err := a.Registry.ExecuteResult(a, "read_mcp_resource", `{"server":"fixture","uri":"test://one"}`)
	if err != nil || !strings.Contains(resources.Output, "resource content") {
		t.Fatalf("resource read: %+v %v", resources, err)
	}
	a.McpClients["fixture"].handleMessage(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	if _, err := a.Registry.ExecuteResult(a, definitions[0].Function.Name, `{}`); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatal(fmt.Sprintf("integration did not reconnect after catalog change: %d", starts.Load()))
	}
}
