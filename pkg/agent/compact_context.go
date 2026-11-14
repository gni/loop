package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"loop/pkg/agent/run"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/domain/message"
)

func isContextSummary(msg db.Message) bool {
	return msg.Role == "system" || msg.IsContextSummary()
}

// compactionTranscript reserves space for prior summaries and user instructions,
// then fills the remaining budget from the newest execution evidence. Output
// stays chronological, with explicit gaps instead of silently dropping the tail.
func compactionTranscript(messages []db.Message, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	messages = compactRuntimeMessages(messages)
	const gapNotice = "[earlier transcript records omitted to fit local context]\n\n"
	const tailNotice = "[transcript records omitted to fit local context]\n"
	selected := make(map[int]string)
	remaining := maxBytes - len(tailNotice)
	selectMessage := func(index, maxAllocation int) {
		if _, exists := selected[index]; exists || maxAllocation <= 0 {
			return
		}
		_, leftSelected := selected[index-1]
		_, rightSelected := selected[index+1]
		leftGap := index > 0 && !leftSelected
		rightGap := index+1 < len(messages) && !rightSelected
		// Selecting a record splits, shrinks or closes an omitted run. Account
		// for that exact marker change before allocating its content.
		overhead := 2
		if leftGap && rightGap {
			overhead += len(gapNotice)
		} else if !leftGap && !rightGap {
			overhead -= len(gapNotice)
		}
		if index == len(messages)-1 {
			overhead += len(gapNotice) - len(tailNotice)
		}
		contentBudget := min(maxAllocation, remaining) - overhead
		if contentBudget <= 0 {
			return
		}
		segment := compactionSegment(messages[index], contentBudget)
		if segment == "" {
			return
		}
		segment = truncateTranscriptSegment(segment, contentBudget)
		selected[index] = segment
		remaining -= len(segment) + overhead
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
			selectMessage(index, min(1600, instructionBudget-(beforeInstructions-remaining)))
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		selectMessage(index, 1200)
	}
	var transcript strings.Builder
	omitted := false
	for index := range messages {
		if segment, exists := selected[index]; exists {
			if omitted {
				transcript.WriteString(gapNotice)
				omitted = false
			}
			transcript.WriteString(segment)
			transcript.WriteString("\n\n")
		} else {
			omitted = true
		}
	}
	if omitted {
		transcript.WriteString(tailNotice)
	}
	return truncateTranscriptSegment(transcript.String(), maxBytes)
}

func compactionSegment(msg db.Message, maxBytes int) string {
	if isSkillInstruction(msg) {
		return fmt.Sprintf("Loaded skill %q: instructions retained separately.", msg.SkillName)
	}
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
		detail := msg.Content
		outcome := msg.ToolOutcome
		if msg.Evidence != nil {
			outcome = msg.Evidence.Outcome
			// Timing belongs to executor metadata, not the narrative summary.
			record := *msg.Evidence
			record.DurationMS, record.MutationAt = nil, 0
			record.Checks = slices.Clone(record.Checks)
			for i := range record.Checks {
				record.Checks[i].StartedAt = 0
			}
			observed, _ := json.Marshal(record)
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
	matches := message.MatchToolResults(messages)
	for index, msg := range messages {
		if isContextSummary(msg) {
			collectPriorSummaryFiles(msg.Content, reads, changes)
		}
		if msg.Role != "tool" {
			continue
		}
		match, exists := matches[index]
		if !exists {
			continue
		}
		call := match.Call
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
	objective := run.Objective(ctx)
	if objective == "" && len(instructions) > 0 {
		objective = instructions[0]
	}
	for _, instruction := range instructions {
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
		if instruction == objective {
			kind = "objective"
		}
		pinned = append(pinned, db.Message{Role: "user", ContextKind: kind, Content: instruction})
	}
	return pinned
}
