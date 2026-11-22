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
	if !strings.Contains(prompt, "run the narrowest relevant build, linter, or test checks permitted") {
		t.Errorf("expected test verification in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Deliver complete, runnable code with no placeholders, stubs, or TODOs.") {
		t.Errorf("expected complete code in core rules, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Use todo only for multi-step work that benefits from tracking") {
		t.Errorf("expected todo rule in core rules, got:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_OperatingLoop(t *testing.T) {
	for _, compact := range []bool{false, true} {
		for _, native := range []bool{false, true} {
			prompt := BuildSystemPrompt(SystemPromptConfig{
				CompactPrompt:     compact,
				NativeToolSchemas: native,
				BaseInstruction:   "Custom engineering role",
			})
			for _, fragment := range []string{
				"<role>\nCustom engineering role\n</role>",
				"<operating_loop>", "1. DISCOVER:", "2. PLAN:", "3. EDIT:", "4. VERIFY:", "5. REPORT:", "</operating_loop>",
				"Never claim a check passed without tool output; explicitly note anything unverified.",
				"Ask for explicit approval before destructive actions, irreversible git operations, or changes outside the workspace.",
				"Terminal command execution is unavailable",
			} {
				if !strings.Contains(prompt, fragment) {
					t.Errorf("compact=%v native=%v: missing %q", compact, native, fragment)
				}
			}
		}
	}
}
