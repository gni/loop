package agent

import (
	"encoding/json"
	"loop/pkg/db"
	"strings"
	"testing"
)

func TestEnforceToolPairingInvariance(t *testing.T) {
	// Case 1: Normal paired conversation -> unchanged
	messages := []db.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Run tests"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_1", Function: db.ToolFunction{Name: "bash"}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "bash", Content: "PASS"},
	}

	result := EnforceToolPairingInvariance(messages)
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}
	if result[3].ToolCallID != "call_1" {
		t.Errorf("expected tool_call_id call_1, got %s", result[3].ToolCallID)
	}

	// Case 2: Interrupted turn with missing tool response
	interrupted := []db.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Run compound"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_1", Function: db.ToolFunction{Name: "bash"}},
				{ID: "call_2", Function: db.ToolFunction{Name: "read"}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "bash", Content: "PASS"},
		// call_2 response is missing!
		{Role: "user", Content: "Next step"},
	}

	fixed := EnforceToolPairingInvariance(interrupted)
	// Should insert placeholder for call_2 before "Next step"
	if len(fixed) != 6 {
		t.Fatalf("expected 6 messages with inserted tool response, got %d", len(fixed))
	}
	if fixed[4].Role != "tool" || fixed[4].ToolCallID != "call_2" {
		t.Errorf("expected inserted tool message for call_2, got %+v", fixed[4])
	}

	// Case 3: Orphan tool message without preceding assistant tool call
	orphan := []db.Message{
		{Role: "system", Content: "System prompt"},
		// Earlier assistant message was evicted/dropped
		{Role: "tool", ToolCallID: "call_orphan", Name: "bash", Content: "lost output"},
		{Role: "user", Content: "What is next?"},
	}

	fixedOrphan := EnforceToolPairingInvariance(orphan)
	// Should remove orphan tool message
	if len(fixedOrphan) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d: %+v", len(fixedOrphan), fixedOrphan)
	}
	if fixedOrphan[1].Role != "user" {
		t.Errorf("expected user message, got %s", fixedOrphan[1].Role)
	}
}

func TestParallelToolCallsSerialization(t *testing.T) {
	f := false
	req := ChatCompletionRequest{
		Model:             "gpt-4",
		ParallelToolCalls: &f,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"parallel_tool_calls":false`) {
		t.Fatalf("expected parallel_tool_calls: false in json, got %s", string(data))
	}

	reqNil := ChatCompletionRequest{
		Model: "gpt-4",
	}
	dataNil, err := json.Marshal(reqNil)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if strings.Contains(string(dataNil), "parallel_tool_calls") {
		t.Fatalf("expected parallel_tool_calls to be omitted when nil, got %s", string(dataNil))
	}
}

