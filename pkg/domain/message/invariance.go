package message

// EnforceToolPairingInvariance keeps exactly one response for each tool call in
// the immediately preceding assistant message. Missing responses are marked as
// interrupted before the next conversation message or the end of history.
func EnforceToolPairingInvariance(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}
	result := make([]Message, 0, len(messages))
	var pending []ToolCall
	unanswered := make(map[string]ToolCall)
	closePending := func() {
		for _, call := range pending {
			if _, ok := unanswered[call.ID]; !ok {
				continue
			}
			result = append(result, Message{
				Role: RoleTool, ToolCallID: call.ID, Name: call.Function.Name,
				Content:     "[tool result unavailable after interruption; execution may have changed files or started processes. Inspect current state before retrying this operation.]",
				ToolOutcome: "unknown",
				IsError:     true,
			})
			delete(unanswered, call.ID)
		}
		pending = nil
	}
	for _, msg := range messages {
		if msg.Role == RoleTool {
			if _, ok := unanswered[msg.ToolCallID]; ok {
				result = append(result, msg)
				delete(unanswered, msg.ToolCallID)
			}
			continue
		}
		closePending()
		result = append(result, msg)
		if msg.Role == RoleAssistant {
			pending = msg.ToolCalls
			for _, call := range pending {
				unanswered[call.ID] = call
			}
		}
	}
	closePending()
	return result
}

// RecoverToolHistory makes interrupted saved calls explicit without replaying
// them. Callers can persist the repaired context while retaining source records.
func RecoverToolHistory(messages []Message) ([]Message, bool) {
	repaired := EnforceToolPairingInvariance(messages)
	if len(repaired) != len(messages) {
		return repaired, true
	}
	for index, msg := range repaired {
		original := messages[index]
		if msg.Role != original.Role || msg.ToolCallID != original.ToolCallID || msg.ToolOutcome != original.ToolOutcome {
			return repaired, true
		}
	}
	return repaired, false
}
