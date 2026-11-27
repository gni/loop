package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/message"
	"loop/pkg/domain/task"
)

var editDiffRow = regexp.MustCompile(`(?m)^\x1b\[(?:31|32)m[0-9]+ +[+-] `)

func inferenceMessages(ctx context.Context, messages []db.Message) []db.Message {
	messages = compactRuntimeMessages(messages)
	messages = taskContextMessages(ctx, messages)
	return taskInstructionMessages(ctx, messages)
}

// compactRuntimeMessages applies pure per-message inference formatting without
// retroactively mutating earlier turns, preserving prompt KV-cache prefixes.
// Saved messages, execution evidence, errors and the terminal transcript remain intact.
func compactRuntimeMessages(messages []db.Message) []db.Message {
	matches := message.MatchToolResults(messages)
	out := make([]db.Message, 0, len(messages))
	for i, msg := range messages {
		if msg.Role != "tool" {
			out = append(out, msg)
			continue
		}
		originalContent := msg.Content
		call := matches[i].Call
		name := tool.NormalizeName(msg.Name)
		if name == "" {
			name = tool.NormalizeName(call.Function.Name)
		}
		if !msg.IsError {
			switch name {
			case "edit":
				observed := msg.Evidence
				if observed != nil && observed.Kind == "file" && observed.Outcome == evidence.Succeeded && observed.Changed != nil && *observed.Changed && !historicalToolFailed(msg) {
					// An after-tool hook may replace the diff with its own
					// feedback. Preserve that output for the model.
					if !editDiffRow.MatchString(msg.Content) && !strings.HasPrefix(msg.Content, "Diff exceeds display limits.") && !strings.HasPrefix(msg.Content, "Diff display truncated.") && msg.Content != "(no output)" {
						break
					}
					// The diff stays in saved/UI history. The model already has
					// the replacement arguments; retain only confirmation and
					// any notices appended after the numbered diff rows.
					_, notice, hasNotice := strings.Cut(msg.Content, "\n\n[")
					msg.Content = "Edit successful."
					if hasNotice {
						msg.Content += "\n\n[" + notice
					}
				}
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
	}
	return out
}
