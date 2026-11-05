package agent

import (
	"strings"
	"testing"
)

func TestBuildSystemPrompt_IncludesProjectContext(t *testing.T) {
	cfg := SystemPromptConfig{
		WorkspaceRoot:  "/workspace/testproj",
		ProjectContext: "Tech Stack: Go (module: testproj, go: 1.23)\nPrimary Test Command: go test ./...",
	}

	prompt := BuildSystemPrompt(cfg)
	if !strings.Contains(prompt, "<project_context>") {
		t.Errorf("expected prompt to contain <project_context>, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Tech Stack: Go (module: testproj, go: 1.23)") {
		t.Errorf("expected prompt to contain Tech Stack, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Primary Test Command: go test ./...") {
		t.Errorf("expected prompt to contain Primary Test Command, got:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_ElevatedCoreRules(t *testing.T) {
	cfg := SystemPromptConfig{
		WorkspaceRoot: "/workspace/myrepo",
	}

	prompt := BuildSystemPrompt(cfg)
	if !strings.Contains(prompt, "Verify code changes by running relevant builds or tests") {
		t.Errorf("expected test verification in core rules, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Deliver complete, runnable code without placeholders or stubs") {
		t.Errorf("expected complete code in core rules, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Use todo for multi-step tasks") {
		t.Errorf("expected todo rule in core rules, got:\n%s", prompt)
	}
}
