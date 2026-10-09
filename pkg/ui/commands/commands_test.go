package commands

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func setupTestAgent(t *testing.T) (*agent.Agent, string) {
	t.Helper()
	tmpDir := t.TempDir()
	if err := db.InitDB(tmpDir); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.Config{
		ContextWindowLimit: 128000,
		MinContextWindow:   8192,
		Model:              "test-model",
		Endpoint:           "https://test.invalid",
	}
	_ = config.SaveConfig(cfgPath, cfg)

	a := &agent.Agent{
		Config:        cfg,
		ConfigPath:    cfgPath,
		WorkspaceRoot: tmpDir,
	}
	sessionID := db.NewUUID()
	_ = db.SetLatestSessionID(sessionID)
	return a, sessionID
}

func TestSlashCommands_ExitAndToggle(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	// Test /exit
	handled, quit := HandleSlashCommand(a, "/exit", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || !quit {
		t.Fatalf("expected /exit to return handled=true, quit=true; got %v, %v", handled, quit)
	}

	// Test /toggle
	output.Reset()
	handled, quit = HandleSlashCommand(a, "/toggle", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /toggle to return handled=true, quit=false; got %v, %v", handled, quit)
	}
	if !a.Config.CollapseResults {
		t.Fatalf("expected CollapseResults to be true after toggle")
	}
}

func TestSlashCommands_Task(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	// /task with no tasks
	handled, quit := HandleSlashCommand(a, "/task list", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /task list to be handled")
	}
	if !strings.Contains(output.String(), "no background tasks registered") {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestSlashCommands_Config(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	// Set temperature
	handled, quit := HandleSlashCommand(a, "/config temp 0.7", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /config to be handled")
	}
	if a.Config.Temperature != 0.7 {
		t.Fatalf("expected temperature 0.7, got %v", a.Config.Temperature)
	}

	// Set invalid key
	output.Reset()
	handled, quit = HandleSlashCommand(a, "/config nonexistent_key 123", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /config to be handled")
	}
	if !strings.Contains(output.String(), "unknown config key") {
		t.Fatalf("expected unknown key error, got: %s", output.String())
	}
}

func TestSlashCommands_Debug(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	handled, quit := HandleSlashCommand(a, "/debug", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /debug to be handled")
	}
	if !strings.Contains(output.String(), "debug log") {
		t.Fatalf("unexpected debug output: %s", output.String())
	}
}

func TestSlashCommands_Session(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}
	_ = db.SaveMessage(sessionID, messages[0])
	_ = db.SaveMessage(sessionID, messages[1])

	// List sessions
	handled, quit := HandleSlashCommand(a, "/session list", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /session list to be handled")
	}
	if !strings.Contains(output.String(), sessionID) {
		t.Fatalf("expected session list to contain %s, got: %s", sessionID, output.String())
	}

	// Branch session
	output.Reset()
	newBranch := "branch-123"
	handled, quit = HandleSlashCommand(a, "/session branch "+newBranch, &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /session branch to be handled")
	}
	if sessionID != newBranch {
		t.Fatalf("expected active session to be %s, got %s", newBranch, sessionID)
	}
}

func TestSlashCommands_Skills(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	handled, quit := HandleSlashCommand(a, "/skills", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /skills to be handled")
	}
}

func TestSlashCommands_PluginsAndExtensions(t *testing.T) {
	a, sessionID := setupTestAgent(t)
	a.Registry = tool.NewToolRegistry()
	var output bytes.Buffer
	theme := &style.UITheme{}
	messages := []db.Message{{Role: "system", Content: "sys"}}

	// Plugins (empty)
	handled, quit := HandleSlashCommand(a, "/plugins", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /plugins to be handled")
	}
	if !strings.Contains(output.String(), "no custom plugins registered") {
		t.Fatalf("unexpected plugins output: %s", output.String())
	}

	// Extensions (empty)
	output.Reset()
	handled, quit = HandleSlashCommand(a, "/extensions", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /extensions to be handled")
	}
	if !strings.Contains(output.String(), "no custom slash command extensions found") {
		t.Fatalf("unexpected extensions output: %s", output.String())
	}
}

func TestFieldStartIndex(t *testing.T) {
	tests := []struct {
		s        string
		fieldIdx int
		expected int
	}{
		{"/agent spawn bob hello", 0, 0},
		{"/agent spawn bob hello", 1, 7},
		{"/agent spawn bob hello", 2, 13},
		{"/agent spawn bob hello", 3, 17},
		{"  /agent   spawn bob hello  ", 0, 2},
		{"  /agent   spawn bob hello  ", 1, 11},
		{"  /agent   spawn bob hello  ", 2, 17},
		{"  /agent   spawn bob hello  ", 3, 21},
		{"/agent spawn bob hello", 4, -1},
	}

	for _, tt := range tests {
		got := fieldStartIndex(tt.s, tt.fieldIdx)
		if got != tt.expected {
			t.Errorf("fieldStartIndex(%q, %d) = %d; want %d", tt.s, tt.fieldIdx, got, tt.expected)
		}
	}
}
