package agent

import (
	"loop/pkg/db"
)

func messageChars(m db.Message) int {
	chars := len(m.Content) + len(m.ReasoningContent)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return chars
}

// EstimateMessageTokens provides a calibrated token count estimation for messages.
// It uses an average of 3.2 characters per token to account for code, JSON, and whitespace.
func EstimateMessageTokens(m db.Message) int {
	chars := messageChars(m)
	if chars == 0 {
		return 0
	}
	tokens := int(float64(chars) / 3.2)
	if tokens == 0 && chars > 0 {
		return 1
	}
	return tokens
}

// EstimateMessagesTokens computes the total estimated tokens across a list of messages.
func EstimateMessagesTokens(messages []db.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateMessageTokens(m)
	}
	return total
}

// EstimateFallbackTokens calculates fallback token usage when the provider omitted token counts.
func EstimateFallbackTokens(promptTokens, completionTokens int, messages []db.Message, calls []db.ToolCall, rawText, reasoning string) (int, int) {
	if promptTokens == 0 {
		totalChars := 0
		for _, msg := range messages {
			totalChars += len(msg.Content) + len(msg.ReasoningContent)
			for _, tc := range msg.ToolCalls {
				totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		promptTokens = totalChars / 4
		if promptTokens == 0 && totalChars > 0 {
			promptTokens = 1
		}
	}
	if completionTokens == 0 {
		completionChars := len(rawText) + len(reasoning)
		for _, tc := range calls {
			completionChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		completionTokens = completionChars / 4
		if completionTokens == 0 && completionChars > 0 {
			completionTokens = 1
		}
	}
	return promptTokens, completionTokens
}

// DefaultContextTiers defines progressive context tiers standard in local LLM runtimes (32K -> 64K -> 128K -> 262K).
var DefaultContextTiers = []int{32768, 65536, 131072, 262144}

// GetAdaptiveContextLimit dynamically selects the optimal context tier for the current workload,
// bounded by the server's detected limits and user configuration.
func GetAdaptiveContextLimit(currentTokens int, serverLimit int, configuredLimit int, minWindow int, autoAdapt bool) int {
	maxCeiling := configuredLimit
	if maxCeiling <= 0 {
		maxCeiling = 128000
	}
	if serverLimit > 0 {
		if serverLimit < maxCeiling || configuredLimit == 128000 {
			maxCeiling = serverLimit
		}
	}

	if !autoAdapt {
		return maxCeiling
	}

	if minWindow <= 0 {
		minWindow = 32768
	}
	if minWindow > maxCeiling {
		minWindow = maxCeiling
	}

	reserveTokens := 4096
	neededTokens := currentTokens + reserveTokens

	var tiers []int
	tiers = append(tiers, minWindow)
	for _, t := range DefaultContextTiers {
		if t > minWindow && t <= maxCeiling {
			tiers = append(tiers, t)
		}
	}
	if len(tiers) == 0 || tiers[len(tiers)-1] < maxCeiling {
		tiers = append(tiers, maxCeiling)
	}

	for _, tier := range tiers {
		if tier >= neededTokens {
			if tier > maxCeiling {
				return maxCeiling
			}
			return tier
		}
	}

	return maxCeiling
}

func estimateCompletionTokens(m db.Message) int {
	if m.CompletionTokens > 0 {
		return m.CompletionTokens
	}
	chars := messageChars(m)
	comp := chars / 4
	if comp == 0 && chars > 0 {
		comp = 1
	}
	return comp
}

// CalculateHistoryTokens calculates prompt and completion tokens for a conversation history.
// For each assistant turn, if PromptTokens > 0 it uses it; otherwise it estimates prompt tokens
// from all messages in the history preceding that turn. If CompletionTokens > 0 it uses it;
// otherwise it estimates completion tokens from Content, ReasoningContent, and ToolCalls.
func CalculateHistoryTokens(history []db.Message) (int, int) {
	var totalPrompt, totalCompletion int
	for i, m := range history {
		if m.Role != "assistant" {
			continue
		}
		hasPayload := m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0
		if !hasPayload && m.PromptTokens == 0 && m.CompletionTokens == 0 {
			continue
		}

		// Completion tokens
		totalCompletion += estimateCompletionTokens(m)

		// Prompt tokens
		if m.PromptTokens > 0 {
			totalPrompt += m.PromptTokens
		} else {
			priorChars := 0
			for j := 0; j < i; j++ {
				priorChars += messageChars(history[j])
			}
			p := priorChars / 4
			if p == 0 && priorChars > 0 {
				p = 1
			}
			totalPrompt += p
		}
	}
	return totalPrompt, totalCompletion
}

// GetGlobalTokens returns the prompt and completion tokens from the latest assistant message.
func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	prompt, completion, _ := a.GetGlobalTokenUsage(messages, allowedTools)
	return prompt, completion
}

// GetGlobalTokenUsage extracts measured token counts returned by the OpenAI API from message history.
// If the latest assistant turn only reported completion tokens or if no assistant turn exists yet,
// it computes an estimate based on prompt characters and active tools.
func (a *Agent) GetGlobalTokenUsage(messages []db.Message, _ []string) (int, int, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
		if message.Role == "assistant" && hasPayload && (message.PromptTokens > 0 || message.CompletionTokens > 0) {
			if message.PromptTokens > 0 {
				return message.PromptTokens, message.CompletionTokens, false
			}
			priorChars := 0
			for j := 0; j < i; j++ {
				prior := messages[j]
				priorChars += len(prior.Content) + len(prior.ReasoningContent)
				for _, tc := range prior.ToolCalls {
					priorChars += len(tc.Function.Name) + len(tc.Function.Arguments)
				}
			}
			estPrompt := priorChars / 4
			if estPrompt == 0 && priorChars > 0 {
				estPrompt = 1
			}
			return estPrompt, message.CompletionTokens, true
		}
	}

	if len(messages) > 0 {
		totalChars := 0
		for _, m := range messages {
			totalChars += len(m.Content) + len(m.ReasoningContent)
			for _, tc := range m.ToolCalls {
				totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		estPrompt := totalChars / 4
		if estPrompt == 0 && totalChars > 0 {
			estPrompt = 1
		}
		if estPrompt > 0 {
			return estPrompt, 0, true
		}
	}

	return 0, 0, false
}

// GetSessionTotalCompletionTokens calculates the global sum of completion tokens generated across all assistant turns in the session.
func (a *Agent) GetSessionTotalCompletionTokens(messages []db.Message) int {
	total := 0
	for _, m := range messages {
		if m.Role == "assistant" {
			hasPayload := m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0
			if hasPayload {
				total += estimateCompletionTokens(m)
			}
		}
	}
	return total
}

// GetLatestAssistantCompletionTokens returns the completion tokens of the latest assistant message.
func (a *Agent) GetLatestAssistantCompletionTokens(messages []db.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == "assistant" {
			hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
			if hasPayload && message.CompletionTokens > 0 {
				return message.CompletionTokens
			}
		}
	}
	return 0
}

// GetEffectiveContextLimit calculates the dynamic context window limit considering
// backend detection, active prompt token requirements, and configured boundaries.
func (a *Agent) GetEffectiveContextLimit(currentPromptTokens int) int {
	if a == nil {
		return 128000
	}
	serverLimit := 0
	a.LLMProviderMu.RLock()
	if detector, ok := a.LLMProvider.(ContextLimitDetector); ok {
		serverLimit = detector.GetDetectedContextLimit()
	}
	a.LLMProviderMu.RUnlock()

	autoAdapt := true
	minWindow := 32768
	configuredLimit := 128000
	if a.Config != nil {
		autoAdapt = a.Config.AutoAdaptContext
		if a.Config.MinContextWindow > 0 {
			minWindow = a.Config.MinContextWindow
		}
		if a.Config.ContextWindowLimit > 0 {
			configuredLimit = a.Config.ContextWindowLimit
		}
	}
	return GetAdaptiveContextLimit(currentPromptTokens, serverLimit, configuredLimit, minWindow, autoAdapt)
}
