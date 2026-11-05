package agent

import (
	"strings"
	"testing"

	"loop/pkg/db"
)

func TestCompactHistoricalToolOutputs_SupersededRead(t *testing.T) {
	messages := []db.Message{
		{Role: "user", Content: "Inspect main.go and fix it"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:       "call_read_1",
					Function: db.ToolFunction{Name: "read", Arguments: `{"path": "main.go"}`},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_read_1",
			Name:       "read",
			Content:    "package main\n\nfunc main() {\n\t// Very long 500 lines of code...\n}\n",
		},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:       "call_read_2",
					Function: db.ToolFunction{Name: "read", Arguments: `{"path": "main.go"}`},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_read_2",
			Name:       "read",
			Content:    "package main\n\nfunc main() {\n\t// Second read content\n}\n",
		},
		{
			Role:    "assistant",
			Content: "I have read main.go twice.",
		},
	}

	compacted := CompactHistoricalToolOutputs(messages)
	if len(compacted) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(compacted))
	}

	// First read (call_read_1) should be marked superseded!
	firstReadContent := compacted[2].Content
	if !strings.Contains(firstReadContent, "superseded by subsequent operation") {
		t.Errorf("expected first read to be superseded, got %q", firstReadContent)
	}

	// Second read (call_read_2) is the latest and should NOT be superseded.
	secondReadContent := compacted[4].Content
	if strings.Contains(secondReadContent, "superseded") {
		t.Errorf("expected second read to NOT be superseded, got %q", secondReadContent)
	}
}

func TestCompactHistoricalToolOutputs_ResolvedEditError(t *testing.T) {
	messages := []db.Message{
		{Role: "user", Content: "Edit config.go"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:       "call_edit_1",
					Function: db.ToolFunction{Name: "edit", Arguments: `{"path": "config.go"}`},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_edit_1",
			Name:       "edit",
			Content:    "error: oldText not found in file config.go near line 42 with 50 lines of diff context...",
		},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:       "call_edit_2",
					Function: db.ToolFunction{Name: "edit", Arguments: `{"path": "config.go"}`},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_edit_2",
			Name:       "edit",
			Content:    "Successfully applied replacement to config.go",
		},
		{
			Role:    "assistant",
			Content: "Applied edit cleanly.",
		},
	}

	compacted := CompactHistoricalToolOutputs(messages)
	firstEditContent := compacted[2].Content
	if !strings.Contains(firstEditContent, "failed - resolved by subsequent update") {
		t.Errorf("expected failed edit to be compacted to resolved stub, got %q", firstEditContent)
	}
}
