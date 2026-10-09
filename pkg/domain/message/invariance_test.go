package message

import (
	"testing"
)

func TestEnforceToolPairingInvarianceDomain(t *testing.T) {
	// Case 1: Empty messages
	empty := EnforceToolPairingInvariance(nil)
	if len(empty) != 0 {
		t.Errorf("expected empty messages, got %d", len(empty))
	}

	// Case 2: Orphaned tool call gets synthetic tool response when followed by user message
	messages := []Message{
		{Role: RoleSystem, Content: "sys"},
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "call_1", Function: ToolFunction{Name: "read", Arguments: `{"path":"a.txt"}`}},
			},
		},
		{Role: RoleUser, Content: "next prompt"},
	}

	repaired := EnforceToolPairingInvariance(messages)
	if len(repaired) != 4 {
		t.Fatalf("expected 4 messages after repairing missing tool response, got %d", len(repaired))
	}
	if repaired[2].Role != RoleTool || repaired[2].ToolCallID != "call_1" {
		t.Errorf("expected synthetic tool response at index 2, got %+v", repaired[2])
	}

	// Case 3: Orphaned tool response without matching assistant call is pruned
	orphaned := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleTool, ToolCallID: "ghost_call", Content: "output"},
		{Role: RoleUser, Content: "prompt"},
	}
	pruned := EnforceToolPairingInvariance(orphaned)
	if len(pruned) != 2 {
		t.Fatalf("expected orphaned tool response to be pruned, got %d messages", len(pruned))
	}
	if pruned[1].Role != RoleUser {
		t.Errorf("expected user message at index 1, got %+v", pruned[1])
	}
}
