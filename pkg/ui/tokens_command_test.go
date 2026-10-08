package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestTokensCommand(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			ContextWindowLimit: 128000,
			Model:              "test-model",
		},
	}
	messages := []db.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello world"},
		{Role: "assistant", Content: "Hi there", PromptTokens: 40, CompletionTokens: 10},
	}
	theme := &UITheme{}
	sessionID := "tokens-test"

	for _, cmd := range []string{"/tokens", "/stats", "/token", "/usage"} {
		t.Run(cmd, func(t *testing.T) {
			var output bytes.Buffer
			handled, quit := HandleSlashCommand(
				a,
				cmd,
				&messages,
				nil,
				theme,
				&output,
				&sessionID,
				nil,
				nil,
				nil,
			)

			if !handled || quit {
				t.Fatalf("HandleSlashCommand(%s) = handled %v, quit %v; want true, false", cmd, handled, quit)
			}

			rendered := output.String()
			if !strings.Contains(rendered, "SWARM TOKEN UTILIZATION & COST STATS") {
				t.Fatalf("expected token stats header, got: %s", rendered)
			}
			if !strings.Contains(rendered, "Base Agent (Main)") {
				t.Fatalf("expected Base Agent section, got: %s", rendered)
			}
			if !strings.Contains(rendered, "40") || !strings.Contains(rendered, "10") {
				t.Fatalf("expected 40 prompt and 10 completion tokens, got: %s", rendered)
			}
		})
	}
}

func TestTokensCommandWithMultiAgentManager(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			ContextWindowLimit: 128000,
			Model:              "test-model",
		},
	}
	messages := []db.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi", PromptTokens: 100, CompletionTokens: 20},
	}
	var managerBuf bytes.Buffer
	mam := agent.NewMultiAgentManager(a, &managerBuf, style.UITheme{})
	a.MultiAgentManager = mam

	// Add a subagent directly
	worker := &agent.MultiAgent{
		Name:         "worker",
		SystemPrompt: "Subagent prompt",
		History: []db.Message{
			{Role: "user", Content: "task"},
			{Role: "assistant", Content: "done", PromptTokens: 50, CompletionTokens: 10},
		},
	}
	mam.Agents[worker.Name] = worker

	theme := &UITheme{}
	sessionID := "tokens-mam-test"

	var output bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/tokens",
		&messages,
		nil,
		theme,
		&output,
		&sessionID,
		nil,
		nil,
		nil,
	)

	if !handled || quit {
		t.Fatalf("HandleSlashCommand(/tokens) = handled %v, quit %v; want true, false", handled, quit)
	}

	rendered := output.String()
	if !strings.Contains(rendered, "Total Swarm Utilization") {
		t.Fatalf("expected Total Swarm Utilization, got: %s", rendered)
	}
	if !strings.Contains(rendered, "Subagent: worker") {
		t.Fatalf("expected Subagent: worker, got: %s", rendered)
	}
}

func TestContextCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := tmpDir + "/config.json"
	cfg := &config.Config{
		ContextWindowLimit: 128000,
		AutoAdaptContext:   true,
		MinContextWindow:   32768,
		Model:              "test-model",
	}
	_ = config.SaveConfig(cfgPath, cfg)

	a := &agent.Agent{
		Config:     cfg,
		ConfigPath: cfgPath,
	}
	messages := []db.Message{
		{Role: "user", Content: "Hello world"},
	}
	theme := &UITheme{}
	sessionID := "context-test"

	// 1. /context with no args
	var output bytes.Buffer
	handled, quit := HandleSlashCommand(
		a,
		"/context",
		&messages,
		nil,
		theme,
		&output,
		&sessionID,
		nil,
		nil,
		nil,
	)
	if !handled || quit {
		t.Fatalf("HandleSlashCommand(/context) = handled %v, quit %v; want true, false", handled, quit)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "CONTEXT WINDOW & ADAPTIVE SIZING") {
		t.Fatalf("expected context header, got: %s", rendered)
	}
	if !strings.Contains(rendered, "Effective Active Tier:") {
		t.Fatalf("expected Effective Active Tier, got: %s", rendered)
	}

	// 2. /context off
	output.Reset()
	handled, _ = HandleSlashCommand(a, "/context off", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || a.Config.AutoAdaptContext {
		t.Fatalf("expected AutoAdaptContext=false, got %v", a.Config.AutoAdaptContext)
	}

	// 3. /context auto
	output.Reset()
	handled, _ = HandleSlashCommand(a, "/context auto", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || !a.Config.AutoAdaptContext {
		t.Fatalf("expected AutoAdaptContext=true, got %v", a.Config.AutoAdaptContext)
	}

	// 4. /context 65536
	output.Reset()
	handled, _ = HandleSlashCommand(a, "/context 65536", &messages, nil, theme, &output, &sessionID, nil, nil, nil)
	if !handled || a.Config.ContextWindowLimit != 65536 {
		t.Fatalf("expected ContextWindowLimit=65536, got %d", a.Config.ContextWindowLimit)
	}
}
