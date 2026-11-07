package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	todotool "loop/pkg/agent/tool/todo"
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// restorePlan follows the selected branch, preserving snapshots through context
// compaction. Legacy sessions recover only acknowledged, successful todo calls;
// file edits, shell commands and other side effects are never replayed.
func restorePlan(entries []session.Record) ([]tool.TodoItem, error) {
	var plan []tool.TodoItem
	pending := map[string]string{}
	hasCheckpoint, hasToolHistory := false, false
	replayMessage := func(msg message.Message) {
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				if tool.NormalizeName(call.Function.Name) == "todo" {
					pending[call.ID] = call.Function.Arguments
					hasToolHistory = true
				}
			}
		}
		if msg.Role != "tool" {
			return
		}
		arguments, ok := pending[msg.ToolCallID]
		if !ok {
			return
		}
		delete(pending, msg.ToolCallID)
		text := strings.ToLower(strings.TrimSpace(msg.Content))
		if msg.IsError || strings.HasPrefix(text, "system alert:") || strings.HasPrefix(text, "error:") {
			return
		}
		if updated, err := todotool.ReplayPlan(plan, arguments); err == nil {
			plan = updated
		}
	}
	for _, entry := range entries {
		switch entry.Type {
		case "settings":
			var saved map[string]json.RawMessage
			if err := json.Unmarshal(entry.Data, &saved); err != nil {
				return nil, fmt.Errorf("session settings: %w", err)
			}
			if raw, ok := saved["todos"]; ok {
				if err := json.Unmarshal(raw, &plan); err != nil {
					return nil, fmt.Errorf("session todo state: %w", err)
				}
				hasCheckpoint = true
				// A snapshot may precede the tool-result record that acknowledges it.
				// Those calls are already included and must not be applied twice.
				clear(pending)
			}
		case "", "message":
			replayMessage(entry.Message)
		case "context", "compaction":
			if !hasCheckpoint && !hasToolHistory {
				for _, msg := range entry.Messages {
					replayMessage(msg)
				}
			}
		}
	}
	return plan, nil
}
