package agent

import (
	"testing"

	"loop/pkg/db"
)

func TestPageRecentMessages_NoOpWhenWithinLimit(t *testing.T) {
	msgs := []db.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}

	paged := PageRecentMessages(msgs, 10)
	if len(paged) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(paged))
	}
}

func TestPageRecentMessages_PreservesSystemAndCleanTurnBoundary(t *testing.T) {
	msgs := []db.Message{
		{Role: "system", Content: "system instructions"},
		// Turn 1
		{Role: "user", Content: "Turn 1 user"},
		{Role: "assistant", Content: "Turn 1 assistant"},
		// Turn 2 with tool call
		{Role: "user", Content: "Turn 2 user"},
		{Role: "assistant", Content: "Turn 2 assistant", ToolCalls: []db.ToolCall{{ID: "tc_1"}}},
		{Role: "tool", ToolCallID: "tc_1", Content: "Turn 2 tool output"},
		{Role: "assistant", Content: "Turn 2 completion"},
		// Turn 3
		{Role: "user", Content: "Turn 3 user"},
		{Role: "assistant", Content: "Turn 3 assistant"},
	}

	// Total messages = 9. If limit = 5, simple naive slicing would start at index 4 (assistant with tool calls) or index 5 (tool output),
	// orphaning tc_1. PageRecentMessages must step back to Turn 2's user prompt!
	paged := PageRecentMessages(msgs, 5)

	// Result must retain system prompt at index 0:
	if len(paged) == 0 || paged[0].Role != "system" {
		t.Fatalf("expected leading system message, got: %v", paged)
	}

	// Result must not begin with a tool message or orphaned assistant
	if paged[1].Role != "user" {
		t.Errorf("expected first turn after system to begin with 'user', got %q", paged[1].Role)
	}

	// Turn 2's tool call pairing must be intact
	foundTool := false
	foundAssocCall := false
	for _, m := range paged {
		if m.Role == "tool" && m.ToolCallID == "tc_1" {
			foundTool = true
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.ToolCalls[0].ID == "tc_1" {
			foundAssocCall = true
		}
	}
	if foundTool != foundAssocCall {
		t.Errorf("tool and assistant call pairing broken: foundTool=%v, foundAssocCall=%v", foundTool, foundAssocCall)
	}
}

func TestPageRecentMessages_ZeroOrNegativeLimit(t *testing.T) {
	msgs := []db.Message{
		{Role: "user", Content: "hi"},
	}
	paged := PageRecentMessages(msgs, 0)
	if len(paged) != 1 {
		t.Errorf("expected original slice on 0 limit, got %d", len(paged))
	}
}
