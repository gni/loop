package message

import (
	"encoding/json"
	"testing"
)

func TestMessageRoles(t *testing.T) {
	userMsg := NewUser("hello")
	if !userMsg.IsUser() || userMsg.Role != RoleUser || userMsg.Content != "hello" {
		t.Fatalf("unexpected user message: %+v", userMsg)
	}

	sysMsg := NewSystem("instructions")
	if !sysMsg.IsSystem() || sysMsg.Role != RoleSystem || sysMsg.Content != "instructions" {
		t.Fatalf("unexpected system message: %+v", sysMsg)
	}

	asstMsg := NewAssistant("response")
	if !asstMsg.IsAssistant() || asstMsg.Role != RoleAssistant || asstMsg.Content != "response" {
		t.Fatalf("unexpected assistant message: %+v", asstMsg)
	}

	toolMsg := NewToolResult("call_1", "read", "file contents")
	if !toolMsg.IsTool() || toolMsg.Role != RoleTool || toolMsg.ToolCallID != "call_1" || toolMsg.Name != "read" {
		t.Fatalf("unexpected tool message: %+v", toolMsg)
	}
}

func TestMessageToolCallsAndTokens(t *testing.T) {
	idx := 0
	msg := Message{
		Role:             RoleAssistant,
		PromptTokens:     120,
		CompletionTokens: 45,
		ToolCalls: []ToolCall{
			{
				Index: &idx,
				ID:    "call_99",
				Type:  "function",
				Function: ToolFunction{
					Name:      "edit",
					Arguments: `{"path":"main.go"}`,
				},
			},
		},
	}

	if !msg.HasToolCalls() {
		t.Fatal("expected message to report tool calls present")
	}
	if msg.TotalTokens() != 165 {
		t.Fatalf("expected total tokens 165, got %d", msg.TotalTokens())
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	var roundtrip Message
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}

	if len(roundtrip.ToolCalls) != 1 || roundtrip.ToolCalls[0].Function.Name != "edit" {
		t.Fatalf("unexpected unmarshaled tool calls: %+v", roundtrip.ToolCalls)
	}
}
