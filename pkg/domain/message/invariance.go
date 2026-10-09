package message

// EnforceToolPairingInvariance ensures every tool response has a corresponding assistant tool call,
// and synthetic placeholder tool responses are generated for interrupted turns so API schemas remain valid.
func EnforceToolPairingInvariance(messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	var result []Message
	startIdx := 0
	if messages[0].Role == RoleSystem {
		result = append(result, messages[0])
		startIdx = 1
	}

	for i := startIdx; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == RoleTool {
			hasMatchingCall := false
			for j := len(result) - 1; j >= 0; j-- {
				if result[j].Role == RoleAssistant {
					for _, tc := range result[j].ToolCalls {
						if tc.ID == msg.ToolCallID {
							hasMatchingCall = true
							break
						}
					}
					break
				}
				if result[j].Role != RoleTool {
					break
				}
			}
			if hasMatchingCall {
				result = append(result, msg)
			}
			continue
		}

		// Before appending a non-tool message, check if the preceding assistant message had missing tool responses
		result = closeUnansweredToolCalls(result)

		result = append(result, msg)
	}

	// Final check on the tail of result
	result = closeUnansweredToolCalls(result)

	return result
}

func closeUnansweredToolCalls(result []Message) []Message {
	if len(result) == 0 {
		return result
	}
	lastAsstIdx := -1
	for j := len(result) - 1; j >= 0; j-- {
		if result[j].Role == RoleAssistant {
			lastAsstIdx = j
			break
		}
		if result[j].Role != RoleTool {
			break
		}
	}
	if lastAsstIdx != -1 && len(result[lastAsstIdx].ToolCalls) > 0 {
		answered := make(map[string]bool)
		for k := lastAsstIdx + 1; k < len(result); k++ {
			if result[k].Role == RoleTool {
				answered[result[k].ToolCallID] = true
			}
		}
		for _, tc := range result[lastAsstIdx].ToolCalls {
			if !answered[tc.ID] {
				result = append(result, Message{
					Role:       RoleTool,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    "[Tool execution was interrupted or cancelled before returning]",
				})
			}
		}
	}
	return result
}
