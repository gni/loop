package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/run"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
)

func isContextSummary(msg db.Message) bool {
	return msg.ContextKind == "summary" || msg.Role == "system" ||
		strings.HasPrefix(msg.Content, "[System: Below is a summary of the earlier conversation history:")
}

// compactionTranscript reserves space for prior summaries and user instructions,
// then fills the remaining budget from the newest execution evidence. Output
// stays chronological, with explicit gaps instead of silently dropping the tail.
func compactionTranscript(messages []db.Message, maxBytes int) string {
	messages = compactRuntimeMessages(messages)
	seenTasks := make(map[string]bool)
	for i := len(messages) - 1; i >= 0; i-- {
		if state := messages[i].TaskState; state != nil {
			if seenTasks[state.ID] {
				messages[i].TaskState = nil
			} else {
				seenTasks[state.ID] = true
			}
		}
	}

	selected := make(map[int]string)
	remaining := max(0, maxBytes-512)
	selectMessage := func(index, maxSegmentBytes int) {
		if _, exists := selected[index]; exists || remaining < 128 {
			return
		}
		segment := compactionSegment(messages[index], maxSegmentBytes)
		if segment == "" {
			return
		}
		segment = truncateTranscriptSegment(segment, remaining)
		selected[index] = segment
		remaining -= len(segment)
	}
	// The most recent prior summary carries facts from earlier compactions.
	for index := len(messages) - 1; index >= 0; index-- {
		if isContextSummary(messages[index]) {
			selectMessage(index, min(5000, maxBytes/3))
			break
		}
	}
	instructionBudget := min(remaining, maxBytes/3)
	beforeInstructions := remaining
	for index := len(messages) - 1; index >= 0 && beforeInstructions-remaining < instructionBudget; index-- {
		msg := messages[index]
		if msg.Role == "user" && msg.ContextKind != "runtime" && !isContextSummary(msg) {
			selectMessage(index, 1600)
		}
	}
	for index := len(messages) - 1; index >= 0 && remaining >= 128; index-- {
		selectMessage(index, 1200)
	}
	var transcript strings.Builder
	omitted := false
	for index := range messages {
		if segment, exists := selected[index]; exists {
			if omitted {
				transcript.WriteString("[earlier transcript records omitted to fit local context]\n\n")
				omitted = false
			}
			transcript.WriteString(segment)
			transcript.WriteString("\n\n")
		} else {
			omitted = true
		}
	}
	if omitted {
		transcript.WriteString("[transcript records omitted to fit local context]\n")
	}
	return truncateTranscriptSegment(transcript.String(), maxBytes)
}

func compactionSegment(msg db.Message, maxBytes int) string {
	if isContextSummary(msg) {
		return "Prior Context Summary: " + truncateTranscriptSegment(msg.Content, maxBytes)
	}
	switch msg.Role {
	case "user":
		if msg.ContextKind == "runtime" {
			return ""
		}
		return "User: " + truncateTranscriptSegment(msg.Content, maxBytes)
	case "assistant":
		var output strings.Builder
		if msg.TaskState != nil {
			fmt.Fprintln(&output, run.TaskBrief(msg.TaskState))
		}
		if msg.Content != "" {
			fmt.Fprintf(&output, "Agent: %s\n", truncateTranscriptSegment(msg.Content, maxBytes))
		}
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(&output, "Requested tool: %s(%s)\n", call.Function.Name, truncateTranscriptSegment(call.Function.Arguments, 300))
			if output.Len() > maxBytes {
				break
			}
		}
		return truncateTranscriptSegment(output.String(), maxBytes)
	case "tool":
		status := "returned"
		if msg.IsError {
			status = "failed"
		}
		detail := msg.Content
		outcome := msg.ToolOutcome
		if msg.Evidence != nil {
			outcome = msg.Evidence.Outcome
			observed, _ := json.Marshal(msg.Evidence)
			detail = "Observed tool effects: " + truncateTranscriptSegment(string(observed), min(768, maxBytes/2)) + "\n" + detail
		}
		if msg.IsError && msg.Recovery != nil {
			detail = fmt.Sprintf("Recovery [%s]: %s\nDiagnostic: %s", msg.Recovery.Code, msg.Recovery.Instruction, detail)
		}
		return fmt.Sprintf("Tool %s (%s; outcome %s): %s", msg.Name, status, outcome, truncateTranscriptSegment(detail, maxBytes))
	}
	return ""
}

func observedCompactionFiles(messages []db.Message) (map[string]bool, map[string]bool) {
	reads, changes := make(map[string]bool), make(map[string]bool)
	calls := make(map[string]db.ToolCall)
	for _, msg := range messages {
		if isContextSummary(msg) {
			collectPriorSummaryFiles(msg.Content, reads, changes)
		}
		for _, call := range msg.ToolCalls {
			calls[call.ID] = call
		}
		if msg.Role != "tool" {
			continue
		}
		call, exists := calls[msg.ToolCallID]
		if !exists {
			continue
		}
		path := filetool.ExtractFilePath(call.Function.Arguments)
		if path == "" {
			continue
		}
		switch call.Function.Name {
		case "read":
			if !historicalToolFailed(msg) {
				reads[path] = true
			}
		case "edit", "write":
			if msg.Evidence != nil && msg.Evidence.Changed != nil {
				if *msg.Evidence.Changed {
					changes[path] = true
				}
			} else if !historicalToolFailed(msg) {
				changes[path] = true // Compatibility with older session records.
			}
		}
	}
	for path := range changes {
		delete(reads, path)
	}
	return reads, changes
}

// Retain every explicit update, not only the initial goal, when a long task
// compacts. The same exact instructions then survive saving and reloading.
func pinnedTaskInstructions(ctx context.Context, kept, source []db.Message) []db.Message {
	instructions := run.Instructions(ctx)
	if len(instructions) == 0 {
		start := -1
		for index := range source {
			if source[index].Role == "user" && source[index].ContextKind == "objective" {
				start = index
			}
		}
		if start >= 0 {
			for _, msg := range source[start:] {
				if isPinnedInstruction(msg) {
					instructions = append(instructions, msg.Content)
				}
			}
		}
	}
	var pinned []db.Message
	for index, instruction := range instructions {
		present := false
		for _, msg := range kept {
			if msg.Role == "user" && strings.TrimSpace(msg.Content) == instruction {
				present = true
				break
			}
		}
		if present {
			continue
		}
		kind := "instruction"
		if index == 0 {
			kind = "objective"
		}
		pinned = append(pinned, db.Message{Role: "user", ContextKind: kind, Content: instruction})
	}
	return pinned
}
