package agent

import (
	"context"
	"encoding/json"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/task"
)

func inferenceMessages(ctx context.Context, messages []db.Message) []db.Message {
	messages = compactRuntimeMessages(messages)
	messages = taskInstructionMessages(ctx, messages)
	return taskContextMessages(ctx, messages)
}

// compactRuntimeMessages changes only the inference view. Saved messages,
// execution evidence, errors and the terminal transcript remain available.
func compactRuntimeMessages(messages []db.Message) []db.Message {
	calls := make(map[string]db.ToolCall)
	lastRecap := -1
	for i, msg := range messages {
		if msg.ContextKind == "runtime" && strings.HasPrefix(msg.Content, "[recap ") {
			lastRecap = i
		}
	}
	out := make([]db.Message, 0, len(messages))
	outCalls := make([]db.ToolCall, 0, len(messages))
	for i, msg := range messages {
		for _, call := range msg.ToolCalls {
			calls[call.ID] = call
		}
		if i != lastRecap && msg.ContextKind == "runtime" && strings.HasPrefix(msg.Content, "[recap ") {
			continue
		}
		if msg.Role != "tool" {
			out = append(out, msg)
			outCalls = append(outCalls, db.ToolCall{})
			continue
		}
		originalContent := msg.Content
		call := calls[msg.ToolCallID]
		name := tool.NormalizeName(msg.Name)
		if name == "" {
			name = tool.NormalizeName(call.Function.Name)
		}
		if !msg.IsError {
			switch name {
			case "task_state":
				var args struct {
					Action string `json:"action"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				var state task.State
				var report task.Report
				if task.DecodeProgressReport(msg.Content, &state) == nil && state.Status != "" {
					_ = task.DecodeProgressReport(msg.Content, &report)
					if state.Objective != "" || len(state.Observations) > 0 || state.ID != "" {
						advisory := report.Advisory
						report = task.ProgressReport(&state, args.Action != "update")
						report.Advisory = advisory
					} else if args.Action == "update" {
						report.Summary, report.Plan, report.Remaining = "", nil, nil
					}
					if encoded, err := json.Marshal(report); err == nil {
						msg.Content = string(encoded)
					}
				}
			case "todo":
				var args struct {
					Action   string          `json:"action"`
					TaskID   string          `json:"task_id"`
					Status   string          `json:"status"`
					Task     string          `json:"task"`
					Markdown string          `json:"markdown"`
					Tasks    json.RawMessage `json:"tasks"`
				}
				parsed := json.Unmarshal([]byte(call.Function.Arguments), &args) == nil
				mutates := args.Markdown != "" || len(args.Tasks) > 0 || args.Action == "add" || args.Action == "remove" || args.TaskID != "" || args.Status != "" || args.Task != ""
				if parsed && mutates && args.Action != "list" && args.Action != "get" && args.Action != "show" && args.Action != "view" && args.Action != "read" {
					if strings.HasPrefix(msg.Content, "Updated todo plan:") {
						msg.Content, _, _ = strings.Cut(msg.Content, "\n")
					}
				}
			}
		}
		// MCP results often contain a joined text body and the same text in
		// content blocks. Keep unique text and all non-text blocks, including images.
		if msg.Content != "" && len(msg.ContentBlocks) > 0 {
			var blocks []message.ContentBlock
			for _, block := range msg.ContentBlocks {
				if block.Type == "text" && block.Text != "" && (strings.Contains(originalContent, block.Text) || strings.Contains(msg.Content, block.Text)) {
					continue
				}
				blocks = append(blocks, block)
			}
			msg.ContentBlocks = blocks
		}
		out = append(out, msg)
		outCalls = append(outCalls, call)
	}
	// Older sessions may contain repeated full polling snapshots. Keep the
	// newest identical body and the earlier status/command headers.
	seenOutput := make(map[string]struct{ body, call string })
	for i := len(out) - 1; i >= 0; i-- {
		msg := &out[i]
		if msg.Role != "tool" || msg.IsError || len(msg.ContentBlocks) != 0 {
			continue
		}
		call := outCalls[i]
		if tool.NormalizeName(call.Function.Name) != "get_task_status" {
			continue
		}
		var args struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.TaskID == "" {
			continue
		}
		header, body, ok := strings.Cut(msg.Content, "\n\nOutput:\n")
		if !ok || body == "" || strings.HasPrefix(body, "[New output") || body == "(no new output)" {
			continue
		}
		if prior, exists := seenOutput[args.TaskID]; exists && prior.body == body {
			msg.Content = header + "\n\nOutput:\n(unchanged; output retained in " + prior.call + ")"
		} else {
			seenOutput[args.TaskID] = struct{ body, call string }{body, msg.ToolCallID}
		}
	}
	return out
}
