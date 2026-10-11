package agent

import (
	"os/exec"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/domain/limits"
)

// fakeMCPScript echoes JSON-RPC responses for initialize, tools/list, and tools/call.
// startDelay makes each server cost a measurable amount of boot time so the worker pool
// can be observed.
func fakeMCPScript(startDelay string) string {
	return `
` + startDelay + `
while IFS= read -r line; do
  case "$line" in
    *initialize*) echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake"}}}' ;;
    *tools/list*) echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"explore","description":"fake tool","inputSchema":{"type":"object","properties":{"query":{"type":"string"}}}}]}}' ;;
    *tools/call*) echo '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok-from-stdio"}]}}' ;;
  esac
done
`
}

func testAgent(cfg *config.Config) *Agent {
	a := &Agent{
		Config:         cfg,
		Registry:       tool.NewToolRegistry(),
		McpClients:     make(map[string]*mcpClient),
		McpStartErrors: make(map[string]error),
		mcpPending:     make(map[string]config.MCPServerConfig),
	}
	a.Registry.UnknownToolHook = a.ResolveUnknownTool
	return a
}

// A server without alwaysLoad must not be connected during startup: first paint cannot
// wait for a child process the session may never use.
func TestStartMCPServersDefersServersWithoutAlwaysLoad(t *testing.T) {
	if _, err := lookPathSh(); err != nil {
		t.Skip("sh not available")
	}

	cfg := &config.Config{MCPServers: map[string]config.MCPServerConfig{
		"eager":    {Type: "stdio", Command: "sh", Args: []string{"-c", fakeMCPScript("")}, AlwaysLoad: true},
		"deferred": {Type: "stdio", Command: "sh", Args: []string{"-c", fakeMCPScript("")}},
		"off":      {Type: "stdio", Command: "sh", Args: []string{"-c", fakeMCPScript("")}, Disabled: true},
	}}

	a := testAgent(cfg)
	started := time.Now()
	if err := a.StartMCPServers(cfg.MCPServers); err != nil {
		t.Fatalf("StartMCPServers: %v", err)
	}
	elapsed := time.Since(started)

	if _, ok := a.McpClients["eager"]; !ok {
		t.Error("alwaysLoad server must be connected during startup")
	}
	if _, ok := a.McpClients["deferred"]; ok {
		t.Error("server without alwaysLoad must not be connected during startup")
	}
	if got := a.DeferredMCPServers(); len(got) != 1 || got[0] != "deferred" {
		t.Errorf("deferred list = %v, want [deferred]", got)
	}
	// Disabling must keep a server out of both the connected set and the deferred list.
	for _, name := range a.DeferredMCPServers() {
		if name == "off" {
			t.Error("disabled server must not appear as deferred")
		}
	}

	if elapsed > 2*time.Second {
		t.Errorf("startup took %s for one eager server; first paint must not wait", elapsed)
	}

	a.StopMCPServers()
}

// The worker pool must bound concurrency, not just run things sequentially.
func TestStartMCPServersConnectsEagerServersInParallel(t *testing.T) {
	if _, err := lookPathSh(); err != nil {
		t.Skip("sh not available")
	}

	previous := limits.Master
	defer limits.Set(previous)

	// Six servers at 200ms each: sequential would cost 1.2s, a pool of three costs two
	// waves. The margin is wide so the assertion is about parallelism, not scheduler noise.
	limits.Set(limits.Limits{MCPServerWorkers: 3})

	cfg := &config.Config{MCPServers: map[string]config.MCPServerConfig{}}
	for i := 0; i < 6; i++ {
		cfg.MCPServers[srvName(i)] = config.MCPServerConfig{
			Type:       "stdio",
			Command:    "sh",
			Args:       []string{"-c", fakeMCPScript("sleep 0.2")},
			AlwaysLoad: true,
		}
	}

	a := testAgent(cfg)
	started := time.Now()
	if err := a.StartMCPServers(cfg.MCPServers); err != nil {
		t.Fatalf("StartMCPServers: %v", err)
	}
	elapsed := time.Since(started)

	if len(a.McpClients) != 6 {
		t.Fatalf("connected %d servers, want 6", len(a.McpClients))
	}

	if elapsed >= time.Second+200*time.Millisecond {
		t.Errorf("elapsed %s: no parallelism observed (sequential would be ~1.2s)", elapsed)
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("elapsed %s: faster than two waves of three workers, pool size not respected", elapsed)
	}

	a.StopMCPServers()
}

// A deferred server is reachable through the registry hook: the tool call itself pays
// for the connect instead of startup paying for every server.
func TestDeferredServerConnectsOnFirstToolCall(t *testing.T) {
	if _, err := lookPathSh(); err != nil {
		t.Skip("sh not available")
	}

	cfg := &config.Config{MCPServers: map[string]config.MCPServerConfig{
		"lazy": {Type: "stdio", Command: "sh", Args: []string{"-c", fakeMCPScript("")}},
	}}

	a := testAgent(cfg)
	if err := a.StartMCPServers(cfg.MCPServers); err != nil {
		t.Fatalf("StartMCPServers: %v", err)
	}

	if _, ok := a.Registry.Lookup("mcp__lazy__explore"); ok {
		t.Fatal("deferred server must not have registered tools yet")
	}

	out, err := a.Registry.Execute(nil, "mcp__lazy__explore", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("Execute through the deferred hook: %v", err)
	}
	if out != "ok-from-stdio" {
		t.Fatalf("unexpected output: %q", out)
	}

	if _, ok := a.McpClients["lazy"]; !ok {
		t.Error("first tool call must have connected the deferred server")
	}
	if len(a.DeferredMCPServers()) != 0 {
		t.Errorf("deferred list after connect = %v, want empty", a.DeferredMCPServers())
	}

	a.StopMCPServers()
}

func TestLoadMCPServerRejectsUnknownOrDisabled(t *testing.T) {
	cfg := &config.Config{MCPServers: map[string]config.MCPServerConfig{
		"off": {Type: "stdio", Command: "sh", Disabled: true},
	}}
	a := testAgent(cfg)

	if _, err := a.LoadMCPServer("nope"); err == nil {
		t.Error("unknown server must error")
	}
	if _, err := a.LoadMCPServer("off"); err == nil {
		t.Error("disabled server must error")
	}
}

// A connect in flight under an older generation must not resurrect a server the user
// removed.
func TestSupersededConnectDoesNotLeakIntoLiveMap(t *testing.T) {
	if _, err := lookPathSh(); err != nil {
		t.Skip("sh not available")
	}

	cfg := &config.Config{MCPServers: map[string]config.MCPServerConfig{
		"slow": {Type: "stdio", Command: "sh", Args: []string{"-c", fakeMCPScript("sleep 0.3")}, AlwaysLoad: true},
	}}
	a := testAgent(cfg)

	g := a.mcpGeneration

	// A restart bumps the generation while the connect is in flight, so the connect that
	// finishes afterwards must find itself superseded and clean up rather than publish.
	a.StopMCPServers()

	_, err := a.connectServer("slow", cfg.MCPServers["slow"], g)
	if err == nil {
		t.Error("superseded connect must report that it was superseded")
	}
	if _, ok := a.McpClients["slow"]; ok {
		t.Error("superseded connect published a client into the live map")
	}
	if _, ok := a.Registry.Lookup("mcp__slow__explore"); ok {
		t.Error("superseded connect must not register tools")
	}
}

func srvName(i int) string {
	return string([]byte{'s', '1', 48 + byte(i)})
}

func lookPathSh() (string, error) { return exec.LookPath("sh") }
