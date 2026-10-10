package commands

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func TestHandleSessionSlashCommand(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	messages := []db.Message{}
	theme := &style.UITheme{}
	currentSessionID := "test-session-12345"

	var buf bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/session",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled {
		t.Errorf("expected slash command /session to be handled")
	}
	if quit {
		t.Errorf("expected /session not to quit the REPL")
	}

	got := buf.String()
	expectedActive := "active session: test-session-12345"
	expectedUsage := "usage: /session [list | new | load | branch <new_session_id> | clear]"

	if !strings.Contains(got, expectedActive) {
		t.Errorf("expected output to contain active session message: %q, got: %q", expectedActive, got)
	}
	if !strings.Contains(got, expectedUsage) {
		t.Errorf("expected output to contain usage message: %q, got: %q", expectedUsage, got)
	}

	buf.Reset()
	handled, quit = HandleSlashCommand(
		a,
		"/session invalid-sub-command",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled {
		t.Errorf("expected slash command /session invalid-sub-command to be handled")
	}
	if quit {
		t.Errorf("expected /session invalid-sub-command not to quit the REPL")
	}

	got = buf.String()
	if !strings.Contains(got, expectedActive) {
		t.Errorf("expected invalid subcommand output to contain active session message: %q, got: %q", expectedActive, got)
	}
	if !strings.Contains(got, expectedUsage) {
		t.Errorf("expected invalid subcommand output to contain usage message: %q, got: %q", expectedUsage, got)
	}
}

func TestHandleConfigAndSetCommands(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			MaxCompletionTokens: 100,
		},
	}
	messages := []db.Message{}
	theme := &style.UITheme{}
	currentSessionID := "test-session-12345"

	// 1. Test /config set max_completion_tokens
	var buf bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/config set max_completion_tokens 8192",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled || quit {
		t.Errorf("expected /config set to be handled and not quit")
	}
	if a.Config.MaxCompletionTokens != 8192 {
		t.Errorf("expected MaxCompletionTokens to be 8192, got %d", a.Config.MaxCompletionTokens)
	}

	// 2. Test /set max_tokens
	buf.Reset()
	handled, quit = HandleSlashCommand(
		a,
		"/set max_tokens 4096",
		&messages,
		nil,
		theme,
		&buf,
		&currentSessionID,
		nil,
		nil,
		nil,
	)

	if !handled || quit {
		t.Errorf("expected /set to be handled and not quit")
	}
	if a.Config.MaxCompletionTokens != 4096 {
		t.Errorf("expected MaxCompletionTokens to be 4096, got %d", a.Config.MaxCompletionTokens)
	}
}

func TestSlashCommandQueue(t *testing.T) {
	a := &agent.Agent{Config: &config.Config{}}
	theme := style.GetTheme("catppuccin")
	ki := &interceptor.KeyInterceptorReader{}

	var buf bytes.Buffer
	// Empty queue
	handled, quit := HandleSlashCommand(a, "/queue", nil, nil, &theme, &buf, nil, nil, nil, ki)
	if !handled || quit {
		t.Fatalf("expected handled=true, quit=false, got %v, %v", handled, quit)
	}
	if !strings.Contains(buf.String(), "prompt queue is empty") {
		t.Fatalf("expected empty notice, got: %s", buf.String())
	}

	// Enqueue items
	ki.EnqueuePrompt("fix tests")
	ki.EnqueuePrompt("deploy")

	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/queue", nil, nil, &theme, &buf, nil, nil, nil, ki)
	if !handled || quit {
		t.Fatalf("expected handled=true, quit=false, got %v, %v", handled, quit)
	}
	out := buf.String()
	if !strings.Contains(out, "2 item(s)") || !strings.Contains(out, "fix tests") || !strings.Contains(out, "deploy") {
		t.Fatalf("expected queue list, got: %s", out)
	}

	// Clear queue
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/queue clear", nil, nil, &theme, &buf, nil, nil, nil, ki)
	if !handled || quit {
		t.Fatalf("expected handled=true, quit=false, got %v, %v", handled, quit)
	}
	if !strings.Contains(buf.String(), "cleared 2 queued prompt(s)") {
		t.Fatalf("expected cleared notice, got: %s", buf.String())
	}
	if ki.HasQueuedPrompts() {
		t.Fatal("expected queue to be empty after /queue clear")
	}
}

func TestSlashConfigReasoningOffAndEnable(t *testing.T) {
	a := &agent.Agent{Config: &config.Config{ShowThinking: true, ReasoningEffort: "low"}}
	theme := style.GetTheme("catppuccin")
	var buf bytes.Buffer

	// Turn reasoning off via /config reasoning off
	handled, _ := HandleSlashCommand(a, "/config reasoning off", nil, nil, &theme, &buf, nil, nil, nil, nil)
	if !handled {
		t.Fatal("expected handled=true")
	}
	if a.Config.ReasoningEffort != "off" {
		t.Fatalf("expected off, got effort=%s", a.Config.ReasoningEffort)
	}

	// Turn reasoning back on via /config reasoning low
	buf.Reset()
	handled, _ = HandleSlashCommand(a, "/config reasoning low", nil, nil, &theme, &buf, nil, nil, nil, nil)
	if !handled {
		t.Fatal("expected handled=true")
	}
	if a.Config.ReasoningEffort != "low" {
		t.Fatalf("expected low, got effort=%s", a.Config.ReasoningEffort)
	}

	// Turn thinking off via /config thinking off without affecting reasoning effort
	buf.Reset()
	handled, _ = HandleSlashCommand(a, "/config thinking off", nil, nil, &theme, &buf, nil, nil, nil, nil)
	if !handled {
		t.Fatal("expected handled=true")
	}
	if a.Config.ShowThinking || a.Config.ReasoningEffort != "low" {
		t.Fatalf("expected showThinking=false and effort=low, got showThinking=%v, effort=%s", a.Config.ShowThinking, a.Config.ReasoningEffort)
	}
}

func TestConfigSetPasteThresholds(t *testing.T) {
	a := &agent.Agent{
		Config:     config.DefaultConfig(),
		ConfigPath: "/dev/null",
	}
	theme := &style.UITheme{}
	var out bytes.Buffer
	sessionID := "test"

	handled, _ := HandleSlashCommand(a, "/config set max_paste_lines 42", nil, nil, theme, &out, &sessionID, nil, nil, nil)
	if !handled {
		t.Fatalf("expected /config set max_paste_lines to be handled")
	}
	if a.Config.MaxPasteLines != 42 {
		t.Fatalf("expected MaxPasteLines to be 42, got %d", a.Config.MaxPasteLines)
	}

	handled, _ = HandleSlashCommand(a, "/config set max_paste_chars 1234", nil, nil, theme, &out, &sessionID, nil, nil, nil)
	if !handled {
		t.Fatalf("expected /config set max_paste_chars to be handled")
	}
	if a.Config.MaxPasteChars != 1234 {
		t.Fatalf("expected MaxPasteChars to be 1234, got %d", a.Config.MaxPasteChars)
	}
}
