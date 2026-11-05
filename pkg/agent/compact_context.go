package agent

import (
	"fmt"
	"strings"

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
		return fmt.Sprintf("Tool %s (%s; outcome %s): %s", msg.Name, status, msg.ToolOutcome, truncateTranscriptSegment(msg.Content, maxBytes))
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
		if msg.Role != "tool" || historicalToolFailed(msg) {
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
			reads[path] = true
		case "edit", "write":
			changes[path] = true
		}
	}
	for path := range changes {
		delete(reads, path)
	}
	return reads, changes
}

func pinnedObjective(objective string, kept []db.Message) *db.Message {
	if strings.TrimSpace(objective) == "" {
		return nil
	}
	for _, msg := range kept {
		if msg.Role == "user" && msg.Content == objective {
			return nil
		}
	}
	return &db.Message{Role: "user", ContextKind: "objective", Content: "[Original task; later user instructions take precedence]\n" + objective}
}
