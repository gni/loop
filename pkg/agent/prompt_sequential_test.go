package agent

import (
	"strings"
	"testing"
)

func TestSequentialToolPromptAllowsIndependentBatching(t *testing.T) {
	cfg := SystemPromptConfig{
		BaseInstruction:   "You are an assistant.",
		WorkspaceRoot:     ".",
		CompactPrompt:     true,
		NativeToolSchemas: true,
		SequentialTools:   true,
		Tools:             []ToolEntry{{Name: "write"}, {Name: "read"}},
	}
	prompt := BuildSystemPrompt(cfg)
	if !strings.Contains(prompt, "Independent tool calls may be returned together") {
		t.Fatalf("missing model batching guidance: %s", prompt)
	}
	cfg.SequentialTools = false
	parallelPrompt := BuildSystemPrompt(cfg)
	if strings.Contains(parallelPrompt, "Independent tool calls may be returned together") {
		t.Fatal("parallel mode should not include serial execution guidance")
	}
}
