package message

// ToolResultMatch identifies a call and the assistant step that owns its result.
// Indices refer to messages consumed by the matcher, including non-tool turns.
type ToolResultMatch struct {
	AssistantIndex int
	CallIndex      int
	Call           ToolCall
}

// ToolResultMatcher associates each result once with the preceding assistant's
// call batch. Its zero value is ready to use. Missing or ambiguous IDs cannot
// establish ownership, and another conversation message closes the batch.
type ToolResultMatcher struct {
	next    int
	pending map[string]ToolResultMatch
}

// Next consumes one message and returns its call only for an unambiguous tool
// result in the current batch.
func (m *ToolResultMatcher) Next(msg Message) (ToolResultMatch, bool) {
	index := m.next
	m.next++
	if msg.Role != RoleTool {
		m.Reset()
		if msg.Role == RoleAssistant {
			for callIndex, call := range msg.ToolCalls {
				if call.ID == "" {
					continue
				}
				if m.pending == nil {
					m.pending = make(map[string]ToolResultMatch)
				}
				match := ToolResultMatch{AssistantIndex: index, CallIndex: callIndex, Call: call}
				if _, exists := m.pending[call.ID]; exists {
					match.CallIndex = -1
				}
				m.pending[call.ID] = match
			}
		}
		return ToolResultMatch{}, false
	}
	match, exists := m.pending[msg.ToolCallID]
	delete(m.pending, msg.ToolCallID)
	return match, exists && match.CallIndex >= 0
}

// Reset closes pending calls at a persisted checkpoint without restarting the
// message index. An acknowledgement after that checkpoint is already applied.
func (m *ToolResultMatcher) Reset() {
	clear(m.pending)
}

// MatchToolResults indexes only unambiguous results. It does not alter history
// or synthesize, repeat or remove executions when a result is missing.
func MatchToolResults(messages []Message) map[int]ToolResultMatch {
	var matcher ToolResultMatcher
	var matches map[int]ToolResultMatch
	for index, msg := range messages {
		if match, ok := matcher.Next(msg); ok {
			if matches == nil {
				matches = make(map[int]ToolResultMatch)
			}
			matches[index] = match
		}
	}
	return matches
}
