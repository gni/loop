package agent

import (
	"os/exec"
	"testing"
	"time"

	"loop/pkg/config"
)

// TestMCPStdioRoundTrip exercises the stdio transport against a fake MCP server.
func TestMCPStdioRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	script := `
while IFS= read -r line; do
  case "$line" in
    *initialize*) echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake"}}}' ;;
    *tools/list*) echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"explore","description":"fake tool","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}}' ;;
    *tools/call*) echo '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok-from-stdio"}]}}' ;;
  esac
done
`

	c := &mcpClient{
		name:     "fake",
		config:   config.MCPServerConfig{Type: "stdio", Command: "sh", Args: []string{"-c", script}},
		requests: make(map[int64]chan string),
		nextID:   1,
	}

	if err := c.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.close()

	if !c.initialized {
		t.Fatal("expected initialized after handshake")
	}

	tools, err := c.listTools()
	if err != nil {
		t.Fatalf("listTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "explore" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	out, err := c.callTool("explore", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	if out != "ok-from-stdio" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestMCPStdioStatusDisplay(t *testing.T) {
	c := &mcpClient{
		name:     "codegraph",
		config:   config.MCPServerConfig{Type: "stdio", Command: "codegraph", Args: []string{"serve", "--mcp"}},
		requests: make(map[int64]chan string),
	}
	c.isStdio = true
	c.initialized = true

	if c.config.Command+" "+joinArgs(c.config.Args) != "codegraph serve --mcp" {
		t.Fatal("bad command rendering")
	}
	_ = time.Second
}

func joinArgs(args []string) string {
	var s string
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}
