package agent

import (
	"encoding/json"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
)

func TestPrepareChatCompletionRequest_ContiguousToolMessages(t *testing.T) {
	provider := &OpenAICompatibleProvider{
		Config: &config.Config{
			Model:              "gpt-4o",
			ActiveProvider:     "default",
			ContextWindowLimit: 128000,
		},
	}

	messages := []db.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Inspect the files."},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_1", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"image.png"}`}},
				{ID: "call_2", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"notes.txt"}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Name:       "read",
			Content:    "Read image file image.png",
			ContentBlocks: []message.ContentBlock{
				{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_2",
			Name:       "read",
			Content:    "notes content",
		},
	}

	_, _, jsonData, err := provider.prepareChatCompletionRequest(messages, nil)
	if err != nil {
		t.Fatalf("prepareChatCompletionRequest failed: %v", err)
	}

	var req chatCompletionWireRequest
	if err := json.Unmarshal(jsonData, &req); err != nil {
		t.Fatalf("failed to unmarshal generated wire request: %v", err)
	}

	// Verify that call_1 and call_2 are consecutive tool messages
	var toolIndices []int
	for idx, wm := range req.Messages {
		if wm.Role == "tool" {
			toolIndices = append(toolIndices, idx)
		}
	}

	if len(toolIndices) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(toolIndices))
	}

	if toolIndices[1] != toolIndices[0]+1 {
		t.Fatalf("tool messages are not contiguous: tool_0 at %d, tool_1 at %d (user message was improperly injected between them!)", toolIndices[0], toolIndices[1])
	}

	// Verify that the attached image user message appears AFTER the tool messages
	if len(req.Messages) <= toolIndices[1]+1 {
		t.Fatalf("expected attached image user message following tool messages, but message array ends at %d", len(req.Messages))
	}

	afterToolMsg := req.Messages[toolIndices[1]+1]
	if afterToolMsg.Role != "user" {
		t.Fatalf("expected role 'user' following tool batch, got %q", afterToolMsg.Role)
	}
}
