package agent

import (
	"strings"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
)

// promptTestRegistry mirrors the built-in tool set a real agent registers, so the
// prompt catalog is exercised through the same path the model sees.
func promptTestRegistry() *tool.ToolRegistry {
	r := tool.NewToolRegistry()
	for _, executor := range []tool.ToolExecutor{
		tool.NewBashTool(),
		tool.NewReadTool(),
		tool.NewWriteTool(),
		tool.NewEditTool(),
		tool.NewGrepTool(),
		tool.NewListTool(),
		tool.NewFindTool(),
		tool.NewLoadSkillTool(),
		tool.NewTaskStatusTool(),
		tool.NewTaskKillTool(),
	} {
		r.Register(executor)
	}
	return r
}

// Regression test for the prompt-truncation bug: everything after "Guidelines:"
// in system_instruction was cut off, so user-authored rules never reached the model.
func TestUserGuidelinesSurvivePromptBuild(t *testing.T) {
	a := &Agent{
		Config: &config.Config{
			SystemInstruction: "You are loop.\nGuidelines:\n1. Answer greetings conversationally; do not call tools.",
			SkillsDir:         t.TempDir(),
			CompactPrompt:     true,
		},
		Registry:      promptTestRegistry(),
		WorkspaceRoot: "/workspace",
	}

	prompt := a.GetSystemPrompt()
	if !strings.Contains(prompt, "Answer greetings conversationally") {
		t.Fatalf("user guideline was dropped from the prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "You are loop.") {
		t.Fatalf("identity preamble was lost: %s", prompt)
	}

	// The tool catalog must come from the live registry, not a hardcoded list,
	// so plugin and MCP tools are visible to the model.
	if !strings.Contains(prompt, "- bash:") || !strings.Contains(prompt, "- task_status:") {
		t.Fatalf("tool catalog is missing registry tools:\n%s", prompt)
	}
}

// The prompt must be deterministic between turns so provider-side prompt caching
// is not defeated by map iteration order.
func TestSystemPromptIsDeterministic(t *testing.T) {
	a := &Agent{
		Config:        &config.Config{SystemInstruction: "base", SkillsDir: t.TempDir()},
		Registry:      promptTestRegistry(),
		WorkspaceRoot: "/workspace",
	}
	first := a.GetSystemPrompt()
	for i := 0; i < 20; i++ {
		if a.GetSystemPrompt() != first {
			t.Fatal("system prompt is not stable across turns")
		}
	}
}

type dummyCreateSubagentTool struct{}

func (d *dummyCreateSubagentTool) Name() string          { return "create_subagent" }
func (d *dummyCreateSubagentTool) PromptSnippet() string { return "Create a specialized subagent" }
func (d *dummyCreateSubagentTool) Definition() tool.Tool {
	return tool.Tool{Function: tool.FunctionDefinition{Name: "create_subagent"}}
}
func (d *dummyCreateSubagentTool) Execute(ctx tool.AgentContext, args string) (string, error) {
	return "", nil
}

// The skill catalog reaches every agent because load_skill resolves names from the
// same registry, but delegation rules are only stated to agents whose effective
// tool catalog actually contains spawn_subagent.
func TestSubagentDelegationRulesOnlyForSpawners(t *testing.T) {
	spawner := &Agent{
		Config:        &config.Config{SystemInstruction: "base", SkillsDir: t.TempDir()},
		Registry:      promptTestRegistry(),
		WorkspaceRoot: "/workspace",
	}
	spawner.Registry.Register(&dummyCreateSubagentTool{})

	prompt := spawner.GetSystemPrompt()
	for _, expected := range []string{
		"Subagent skill assignment",
		"Do not invent reference skill names",
		"use 'create_subagent' to create specialized agents",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("spawning agent missing %q:\n%s", expected, prompt)
		}
	}

	var restricted []string
	for _, entry := range promptToolEntries(spawner.Registry) {
		if entry.Name != "create_subagent" && entry.Name != "spawn_subagent" {
			restricted = append(restricted, entry.Name)
		}
	}
	child := spawner.GetSystemPromptFor(restricted)
	if !strings.Contains(child, "Subagent skill assignment") {
		t.Fatalf("non-spawning agent lost the skill catalog:\n%s", child)
	}
	if strings.Contains(child, "use 'create_subagent'") || strings.Contains(child, "use 'spawn_subagent'") || strings.Contains(child, "audit_subagent") || strings.Contains(child, "swarm_audit") {
		t.Fatalf("non-spawning agent was given delegation rules it cannot execute:\n%s", child)
	}
}

func TestCompactPromptExplainsEmptySkillCatalog(t *testing.T) {
	baseAgent := &Agent{
		Config: &config.Config{
			CompactPrompt:     true,
			SystemInstruction: "base",
		},
		WorkspaceRoot: t.TempDir(),
	}

	prompt := baseAgent.GetSystemPrompt()
	if !strings.Contains(prompt, "No registered reference skills are currently installed") {
		t.Fatalf("compact prompt omits the empty skill catalog: %q", prompt)
	}
	if !strings.Contains(prompt, "inline_skills") {
		t.Fatalf("compact prompt omits inline skill guidance: %q", prompt)
	}
}
