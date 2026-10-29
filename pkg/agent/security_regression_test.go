package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"io"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func newSecurityTestAgent(t *testing.T) (*Agent, string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "settings", "config.json")
	cfg := config.DefaultConfig()
	cfg.DisableLocalPlugins = true
	a := NewAgentAt(cfg, configPath, nil, workspace)
	t.Cleanup(func() { _ = a.Dispose(context.Background()) })
	return a, configPath, workspace
}

func TestWorkspaceTrustGatesShellAndBackgroundTasks(t *testing.T) {
	a, configPath, workspace := newSecurityTestAgent(t)
	if a.IsShellExecutionAllowed() {
		t.Fatal("new workspace must not be trusted")
	}
	if _, err := a.SpawnTask("echo forbidden", os.Stderr); err == nil {
		t.Fatal("background task bypassed workspace trust")
	}
	if err := config.TrustWorkspace(configPath, workspace, true); err != nil {
		t.Fatal(err)
	}
	if !a.IsShellExecutionAllowed() {
		t.Fatal("saved trust was not recognized")
	}
	if err := config.TrustWorkspace(configPath, workspace, false); err != nil {
		t.Fatal(err)
	}
	if a.IsShellExecutionAllowed() {
		t.Fatal("revocation did not take effect")
	}
}

func TestBackgroundTaskStoppedOnAgentCancellation(t *testing.T) {
	a, configPath, workspace := newSecurityTestAgent(t)
	if err := config.TrustWorkspace(configPath, workspace, true); err != nil {
		t.Fatal(err)
	}
	id, err := a.SpawnTask("sleep 30", os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	a.CancelLifetime()
	deadline := time.After(4 * time.Second)
	for {
		status, _, err := a.GetTaskStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background process survived cancellation")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := a.SpawnTask("echo too-late", os.Stderr); !errors.Is(err, context.Canceled) {
		t.Fatalf("spawn after lifetime cancellation: %v", err)
	}
}

func TestLocalMCPExecutableRequiresWorkspaceTrust(t *testing.T) {
	a, _, _ := newSecurityTestAgent(t)
	a.Config.EnableMCP = true
	_, err := a.startAndRegisterMCPServer("unsafe", config.MCPServerConfig{Command: "/bin/sh"}, 0)
	if err == nil || !strings.Contains(err.Error(), "trusted workspace") {
		t.Fatalf("untrusted MCP subprocess allowed: %v", err)
	}
}

func TestToolSearchNeverConnectsDeferredMCP(t *testing.T) {
	a, _, _ := newSecurityTestAgent(t)
	// A bogus server would fail if search_tools tried to connect to it.
	a.McpClientsMu.Lock()
	a.mcpPending["unstarted"] = config.MCPServerConfig{Command: "/path/does/not/exist"}
	a.McpClientsMu.Unlock()
	result, err := (&toolSearch{agent: a}).Execute(a, `{"query":"unmatched-integration"}`)
	if err != nil {
		t.Fatalf("read-only discovery launched MCP: %v", err)
	}
	if !strings.Contains(result, "load_mcp") {
		t.Fatalf("missing explicit activation guidance: %q", result)
	}
	if pending := a.DeferredMCPServers(); len(pending) != 1 || pending[0] != "unstarted" {
		t.Fatalf("deferred server was consumed: %v", pending)
	}
	if _, err := (&toolSearch{agent: a}).Execute(a, `{"query":"anything","server":"unstarted"}`); err == nil {
		t.Fatal("deferred server must require explicit load_mcp")
	}
	for _, name := range []string{"load_mcp", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"} {
		if !NeedsApproval(name) {
			t.Fatalf("%s can start MCP but lacks approval", name)
		}
	}
	if _, ok := a.ResolveUnknownTool("mcp__unstarted__missing"); ok {
		t.Fatal("unknown MCP tool was resolved without explicit load_mcp")
	}
}

func TestDebugLoggerOmitsPayloads(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "debug.log")
	dl := NewDebugLogger("", logPath)
	if dl == nil {
		t.Fatal("debug logger unavailable")
	}
	secret := "DO_NOT_PERSIST_MY_SECRET_123"
	dl.LogUserCommand("session", secret)
	dl.LogLLMRequest("session", 1, "local", "http://example", []db.Message{{Role: "user", Content: secret}}, nil)
	dl.LogLLMResponse("session", 1, &db.Message{Role: "assistant", Content: secret, ReasoningContent: secret}, time.Second)
	dl.LogToolExecution("session", 1, "write", secret, secret, errors.New(secret), time.Millisecond)
	dl.LogRepetition("session", "write", secret, 2, secret)
	dl.LogError("session", "persist", errors.New(secret))
	dl.Close()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("secret leaked to debug log: %s", b)
	}
	if !strings.Contains(string(b), "content_bytes=") || !strings.Contains(string(b), "TOOL session=") {
		t.Fatalf("diagnostic metadata missing: %q", b)
	}
	if info, err := os.Stat(logPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe debug file permissions: %v %v", info, err)
	}
}

func TestSessionSurvivesRestart(t *testing.T) {
	a, _, _ := newSecurityTestAgent(t)
	storeDir := filepath.Join(t.TempDir(), "sessions")
	store, err := db.NewJSONLStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	a.Store = store
	sessionID := db.NewUUID()
	messages := []db.Message{{Role: "system", Content: "base"}, {Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}}
	for _, m := range messages {
		if err := a.SaveMessage(sessionID, m); err != nil {
			t.Fatal(err)
		}
	}
	// New store instance simulates reopening the session after a process restart.
	reopened, err := db.NewJSONLStore(filepath.Join(filepath.Dir(storeDir), filepath.Base(storeDir)))
	if err != nil {
		t.Fatal(err)
	}
	a.Store = reopened
	restored, err := a.LoadSessionMessages(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != len(messages) {
		t.Fatalf("restored %d messages, expected %d", len(restored), len(messages))
	}
	for i, want := range messages {
		if restored[i].Role != want.Role || restored[i].Content != want.Content {
			t.Fatalf("message %d mismatch", i)
		}
	}
}

func TestPermissionChangesRemainReversible(t *testing.T) {
	a, _, _ := newSecurityTestAgent(t)
	a.DisableTools("ask_user")
	if !a.IsToolDisabled("ask_user") {
		t.Fatal("ask_user still enabled")
	}
	a.EnableTools("ask_user")
	if a.IsToolDisabled("ask_user") {
		t.Fatal("ask_user still disabled")
	}
	a.EnableTools("list_subagents")
	if a.IsToolDisabled("list_subagents") || !a.IsToolDisabled("create_subagent") {
		t.Fatal("subagent feature enabled more than requested")
	}
	a.EnableTools("search_tools")
	if a.IsToolDisabled("search_tools") || !a.IsToolDisabled("load_mcp") {
		t.Fatal("MCP feature enabled more than requested")
	}
}

func TestCancelledFileMutationPreservesTarget(t *testing.T) {
	a, _, workspace := newSecurityTestAgent(t)
	target := filepath.Join(workspace, "existing.md")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.SetTurnState(io.Discard, ctx, style.GetTheme("plain"))
	if err := filetool.CommitFileMutation(a, target, []byte("new"), 0644); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled commit, got %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatalf("cancelled write changed target: %q %v", data, err)
	}
}
