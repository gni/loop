package agent

import (
	"strings"
	"testing"

	"loop/pkg/db"
	"loop/pkg/domain/evidence"
)

func TestAllocateContext_PreservesLatestAssistantOverStaleInterruptedTool(t *testing.T) {
	interrupted := "error: tool execution interrupted (cancelled during execution)\n\nOutput collected before interruption:\n" +
		strings.Repeat("<unknown>:210: SyntaxWarning: invalid escape sequence\n", 12) + "\n(command cancelled by user)"

	messages := []db.Message{
		{Role: "system", Content: "System instructions."},
		{Role: "user", Content: "Run old analysis " + strings.Repeat("x", 600)},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_bash_interrupted", Type: "function", Function: db.ToolFunction{Name: "bash", Arguments: `{"command":"python3 analyze.py"}`}},
			},
		},
		{
			Role:        "tool",
			ToolCallID:  "call_bash_interrupted",
			Name:        "bash",
			Content:     interrupted,
			ToolOutcome: evidence.Unknown,
			IsError:     true,
			Evidence:    &evidence.Record{Kind: "command", Target: "python3 analyze.py", Outcome: evidence.Unknown},
		},
		{Role: "user", Content: "we were working on architecture migration"},
		{Role: "assistant", Content: "Yes. We are continuing the architecture migration and waiting for tool access."},
		{Role: "user", ContextKind: "instruction", Content: "use tool call for finish migration with edit, write etc.."},
	}

	// Choose a limit that forces eviction of earlier turns while easily fitting
	// system + latest Q&A + latest user input.
	sysTok := EstimateMessageTokens(messages[0])
	recentTok := EstimateMessageTokens(messages[4]) + EstimateMessageTokens(messages[5]) + EstimateMessageTokens(messages[6])
	limit := sysTok + recentTok + 20

	trimmed := allocateContext(messages, limit)

	lastAssistantIdx := -1
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx < 0 {
		t.Fatalf("expected latest assistant message to remain in trimmed context, got %+v", trimmed)
	}
	if len(trimmed[lastAssistantIdx].ToolCalls) != 0 || !strings.Contains(trimmed[lastAssistantIdx].Content, "continuing the architecture migration") {
		t.Fatalf("expected plain-text latest assistant reply as last assistant message, got role=%s content=%q tool_calls=%d",
			trimmed[lastAssistantIdx].Role, trimmed[lastAssistantIdx].Content, len(trimmed[lastAssistantIdx].ToolCalls))
	}

	for _, msg := range trimmed[lastAssistantIdx+1:] {
		if msg.Role == "tool" {
			t.Fatalf("stale tool output %q must not appear after the latest assistant turn", msg.ToolCallID)
		}
	}
	for _, msg := range trimmed {
		if msg.ToolCallID == "call_bash_interrupted" {
			t.Fatalf("expected stale interrupted tool call from earlier turn to be evicted under tight budget")
		}
	}
}

func TestAllocateContext_RetainsInterruptedToolWhenItIsLatestAssistantStep(t *testing.T) {
	messages := []db.Message{
		{Role: "system", Content: "System instructions."},
		{Role: "user", Content: "Older chatter " + strings.Repeat("a", 800)},
		{Role: "assistant", Content: "Older answer " + strings.Repeat("b", 800)},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_bash_interrupted", Type: "function", Function: db.ToolFunction{Name: "bash", Arguments: `{"command":"python3 analyze.py"}`}},
			},
		},
		{
			Role:        "tool",
			ToolCallID:  "call_bash_interrupted",
			Name:        "bash",
			Content:     "error: tool execution interrupted (cancelled during execution)\n\nOutput collected before interruption:\npartial output\n\n(command cancelled by user)",
			ToolOutcome: evidence.Unknown,
			IsError:     true,
		},
		{Role: "user", ContextKind: "instruction", Content: "we were working on architecture migration"},
	}

	limit := EstimateMessageTokens(messages[0]) +
		EstimateMessageTokens(messages[3]) +
		EstimateMessageTokens(messages[4]) +
		EstimateMessageTokens(messages[5]) + 20

	trimmed := allocateContext(messages, limit)

	foundInterrupted := false
	for _, msg := range trimmed {
		if msg.ToolCallID == "call_bash_interrupted" {
			foundInterrupted = true
		}
	}
	if !foundInterrupted {
		t.Fatalf("expected interrupted bash result to remain on the immediate follow-up turn before any later assistant response")
	}
}

func TestAllocateContext_KeepsActiveTaskFailureRequiredAndAllowsPriorFailuresToEvict(t *testing.T) {
	messages := []db.Message{
		{Role: "system", Content: "System instructions."},
		{Role: "user", Content: "Old task"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_old_fail", Type: "function", Function: db.ToolFunction{Name: "bash", Arguments: `{"command":"false"}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_old_fail",
			Name:       "bash",
			Content:    "command failed: exit status 1\n" + strings.Repeat("old failure noise\n", 40),
			IsError:    true,
			Evidence:   &evidence.Record{Kind: "command", Target: "false", Outcome: evidence.Failed},
		},
		{Role: "assistant", Content: "Old task wrapped up."},
		{Role: "user", ContextKind: "instruction", Content: "Current task"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_active_fail", Type: "function", Function: db.ToolFunction{Name: "edit", Arguments: `{"path":"main.go"}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_active_fail",
			Name:       "edit",
			Content:    "error: oldText not found",
			IsError:    true,
			Evidence:   &evidence.Record{Kind: "file", Target: "main.go", Outcome: evidence.Failed},
		},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_active_read", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"main.go"}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_active_read",
			Name:       "read",
			Content:    "package main\nfunc main() {}\n",
		},
	}

	// Budget fits system + current task (user + active fail + active read), not old failure noise.
	limit := EstimateMessageTokens(messages[0]) +
		EstimateMessageTokens(messages[5]) +
		EstimateMessageTokens(messages[6]) +
		EstimateMessageTokens(messages[7]) +
		EstimateMessageTokens(messages[8]) +
		EstimateMessageTokens(messages[9]) + 20

	trimmed := allocateContext(messages, limit)

	var hasOldFail, hasActiveFail, hasActiveRead bool
	for _, msg := range trimmed {
		switch msg.ToolCallID {
		case "call_old_fail":
			hasOldFail = true
		case "call_active_fail":
			hasActiveFail = true
		case "call_active_read":
			hasActiveRead = true
		}
	}
	if hasOldFail {
		t.Fatalf("expected prior-task failure to be evictable under tight budget")
	}
	if !hasActiveFail {
		t.Fatalf("expected unresolved failure within the current active task to remain required")
	}
	if !hasActiveRead {
		t.Fatalf("expected latest active step to remain required")
	}
}
