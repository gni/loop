package agent

import (
	"strings"
	"testing"

	"loop/pkg/config"
)

func TestAgentToolDisablingAndPrompt(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DisabledTools = []string{"bash", "ask_user"}

	a := NewAgent(cfg, "", nil)

	// Verify that bash and ask_user are disabled
	if !a.IsToolDisabled("bash") {
		t.Fatalf("expected bash to be disabled")
	}
	if !a.IsToolDisabled("ask_user") {
		t.Fatalf("expected ask_user to be disabled")
	}
	if a.IsToolDisabled("read") {
		t.Fatalf("expected read to be enabled")
	}

	// Verify system prompt does not contain disabled tools
	prompt := a.GetSystemPrompt()
	if strings.Contains(prompt, "bash: ") {
		t.Fatalf("system prompt should not list disabled tool 'bash':\n%s", prompt)
	}
	if strings.Contains(prompt, "ask_user: ") {
		t.Fatalf("system prompt should not list disabled tool 'ask_user':\n%s", prompt)
	}
	if !strings.Contains(prompt, "read: ") {
		t.Fatalf("system prompt should list enabled tool 'read':\n%s", prompt)
	}

	// Enable bash at runtime
	a.EnableTools("bash")
	if a.IsToolDisabled("bash") {
		t.Fatalf("expected bash to be enabled after EnableTools")
	}
	newPrompt := a.GetSystemPrompt()
	if !strings.Contains(newPrompt, "bash: ") {
		t.Fatalf("system prompt should now list 'bash' after being enabled")
	}

	// Disable read at runtime
	a.DisableTools("read")
	if !a.IsToolDisabled("read") {
		t.Fatalf("expected read to be disabled")
	}
	promptAfterReadDisabled := a.GetSystemPrompt()
	if strings.Contains(promptAfterReadDisabled, "read: ") {
		t.Fatalf("system prompt should no longer list 'read'")
	}

	// Enable all tools
	a.EnableAllTools()
	if a.IsToolDisabled("read") || a.IsToolDisabled("ask_user") {
		t.Fatalf("expected all tools to be enabled after EnableAllTools")
	}
	allPrompt := a.GetSystemPrompt()
	if !strings.Contains(allPrompt, "read: ") || !strings.Contains(allPrompt, "ask_user: ") {
		t.Fatalf("system prompt should list all re-enabled tools")
	}
}

func TestAgentApplyConfigDisabledTools(t *testing.T) {
	cfg := config.DefaultConfig()
	a := NewAgent(cfg, "", nil)

	if a.IsToolDisabled("edit") {
		t.Fatalf("expected edit to be enabled initially")
	}

	nextCfg := config.DefaultConfig()
	nextCfg.DisabledTools = []string{"edit"}
	a.ApplyConfig(nextCfg)

	if !a.IsToolDisabled("edit") {
		t.Fatalf("expected edit to be disabled after ApplyConfig")
	}

	// Next config with no disabled tools
	emptyCfg := config.DefaultConfig()
	emptyCfg.DisabledTools = nil
	a.ApplyConfig(emptyCfg)

	if a.IsToolDisabled("edit") {
		t.Fatalf("expected edit to be re-enabled after ApplyConfig with empty DisabledTools")
	}
}
