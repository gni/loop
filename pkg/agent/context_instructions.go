package agent

import (
	"context"
	"fmt"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/db"
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
	for index := range output {
		msg := &output[index]
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if msg.ContextKind == "objective" {
			content = strings.TrimPrefix(content, "[Original task; later user instructions take precedence]\n")
		}
		if _, current := needed[content]; current {
			needed[content] = false
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
	var retained strings.Builder
	retained.WriteString("User task and updates retained verbatim, in order. Later user instructions take precedence:\n")
	for index, instruction := range instructions {
		fmt.Fprintf(&retained, "\n--- User instruction %d ---\n%s\n", index+1, instruction)
	}
	for index := range output {
		if isPinnedInstruction(output[index]) && len(output[index].ContentBlocks) == 0 {
			output[index].ContextKind = ""
		}
	}
	return append(output, db.Message{Role: "user", ContextKind: "instruction", Content: retained.String()})
}

// Adjacent user messages are coalesced only after budgeting. Doing it earlier
// would merge pinned instructions with unrelated history and prevent eviction.
func mergeAdjacentUserMessages(messages []db.Message) []db.Message {
	output := make([]db.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "user" && len(output) > 0 && output[len(output)-1].Role == "user" {
			previous := &output[len(output)-1]
			if previous.Content == msg.Content && len(msg.ContentBlocks) == 0 {
				continue
			}
			previous.Content += "\n\n" + msg.Content
			previous.ContentBlocks = append(append(previous.ContentBlocks[:0:0], previous.ContentBlocks...), msg.ContentBlocks...)
			continue
		}
		output = append(output, msg)
	}
	return output
}
