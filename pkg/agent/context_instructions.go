package agent

import (
	"context"
	"fmt"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/db"
	"loop/pkg/domain/message"
)

func isPinnedInstruction(msg db.Message) bool {
	return msg.Role == "user" && (msg.ContextKind == "objective" || msg.ContextKind == "instruction")
}

// taskInstructionMessages protects the current task and explicit user updates
// before request budgeting. It adds no inference and never extracts constraints
// by guessing which words in a user message are important.
func taskInstructionMessages(ctx context.Context, messages []db.Message) []db.Message {
	instructions := run.Instructions(ctx)
	if len(instructions) == 0 {
		return messages
	}
	output := append([]db.Message(nil), messages...)
	needed := make(map[string]bool, len(instructions))
	for _, instruction := range instructions {
		needed[instruction] = true
	}
	retained := make(map[int]string, len(instructions))
	for index := len(output) - 1; index >= 0; index-- {
		msg := &output[index]
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if msg.ContextKind == "objective" {
			content = strings.TrimPrefix(content, "[Original task; later user instructions take precedence]\n")
			if msg.TaskState != nil {
				objective := strings.TrimSpace(msg.TaskState.Objective)
				if needed[objective] && strings.HasSuffix(content, "\n\n"+objective) {
					content = objective
				}
			}
		}
		if needed[content] {
			needed[content] = false
			retained[index] = content
			msg.ContextKind = "instruction"
		} else if isPinnedInstruction(*msg) {
			// A prior task's anchor must not crowd out the active task.
			msg.ContextKind = ""
		}
	}
	missing := false
	for _, absent := range needed {
		missing = missing || absent
	}
	if !missing {
		return output
	}

	// Keep the entire ordered instruction sequence together when compaction has
	// removed any original message. Later updates remain authoritative.
	var combined strings.Builder
	combined.WriteString("User task and updates retained verbatim, in order. Later user instructions take precedence:\n")
	for index, instruction := range instructions {
		fmt.Fprintf(&combined, "\n--- User instruction %d ---\n%s\n", index+1, instruction)
	}
	view := output[:0]
	for index, msg := range output {
		// The ordered block replaces these exact instructions. Keep messages
		// carrying attachments in place so their context remains associated.
		if instruction, ok := retained[index]; ok && len(msg.ContentBlocks) == 0 && len(msg.ToolCalls) == 0 {
			content := strings.TrimSpace(msg.Content)
			if !strings.HasSuffix(content, "\n\n"+instruction) {
				continue
			}
			// Preserve integration notices prepended to the objective.
			msg.Content = strings.TrimPrefix(strings.TrimSuffix(content, "\n\n"+instruction), "[Original task; later user instructions take precedence]\n")
			msg.ContextKind = ""
		}
		view = append(view, msg)
	}
	// Insert in the stable prefix slot right after any leading system message
	// (matching pinnedTaskInstructions) rather than moving to the tail on every
	// step, which would break KV cache reuse and merge into XML <tool_result> turns.
	pinned := db.Message{Role: "user", ContextKind: "instruction", Content: combined.String()}
	insertAt := 0
	if len(view) > 0 && view[0].Role == "system" {
		insertAt = 1
	}
	result := make([]db.Message, 0, len(view)+1)
	result = append(result, view[:insertAt]...)
	result = append(result, pinned)
	result = append(result, view[insertAt:]...)
	return result
}

// Adjacent user messages are coalesced only after budgeting. Doing it earlier
// would merge pinned instructions with unrelated history and prevent eviction.
func mergeAdjacentUserMessages(messages []db.Message) []db.Message {
	output := make([]db.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "user" && len(output) > 0 && output[len(output)-1].Role == "user" {
			previous := &output[len(output)-1]
			if previous.Content == msg.Content && len(previous.ContentBlocks) == 0 && len(msg.ContentBlocks) == 0 {
				continue
			}
			if len(previous.ContentBlocks) > 0 {
				// Later steering belongs after the earlier attachments.
				blocks := append(previous.ContentBlocks[:0:0], previous.ContentBlocks...)
				if msg.Content != "" {
					blocks = append(blocks, message.ContentBlock{Type: "text", Text: msg.Content})
				}
				previous.ContentBlocks = append(blocks, msg.ContentBlocks...)
			} else {
				previous.Content += "\n\n" + msg.Content
				previous.ContentBlocks = append(previous.ContentBlocks[:0:0], msg.ContentBlocks...)
			}
			continue
		}
		output = append(output, msg)
	}
	return output
}
