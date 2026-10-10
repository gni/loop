package agent

import (
	"encoding/json"
	"math"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

// charsPerToken is the single calibration used by every estimator in this package.
// EstimateFallbackTokens previously used its own divisor of 4, so the same history
// produced two different numbers depending on which path ran.
const charsPerToken = 3.2

// tokensFromChars converts a character count into the calibrated token estimate.
// Every estimator in the package now routes through this single helper so the same
// history can never produce two different numbers depending on which path ran.
// TokensFromChars is the exported form of the single calibration so streaming
// paths (subagents) report the same number the estimators use.
func TokensFromChars(chars int) int { return tokensFromChars(chars) }

func tokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	// Round rather than truncate: the divisor is not exactly representable in
	// binary floating point, so truncation could report 19 for an exact 64-char
	// window at 3.2 chars/token.
	tokens := int(math.Round(float64(chars) / charsPerToken))
	if tokens == 0 {
		return 1
	}
	return tokens
}

// EstimateToolSchemaTokens estimates the token cost of the tool definitions sent with
// every request. Tool schemas are part of the prompt but were previously invisible to
// the estimator, so per-agent allowlists (subagents) under-counted their own usage.
func EstimateToolSchemaTokens(tools []tool.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	chars := 0
	for _, t := range tools {
		b, err := json.Marshal(t)
		if err != nil {
			chars += len(t.Function.Name) + len(t.Function.Description)
			continue
		}
		chars += len(b)
	}
	return tokensFromChars(chars)
}

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
	return tokensFromChars(messageChars(m))
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
			totalChars += messageChars(msg)
		}
		promptTokens = tokensFromChars(totalChars)
	}
	if completionTokens == 0 {
		completionChars := len(rawText) + len(reasoning)
		for _, tc := range calls {
			completionChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		completionTokens = tokensFromChars(completionChars)
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
	return tokensFromChars(chars)
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
			totalPrompt += tokensFromChars(priorChars)
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
func (a *Agent) GetGlobalTokenUsage(messages []db.Message, allowedTools []string) (int, int, bool) {
	// Tool definitions are sent with every request, so they belong in the prompt
	// estimate. When no allowlist is supplied the estimate stays message-only.
	toolTokens := 0
	if a != nil && a.Registry != nil && len(allowedTools) > 0 {
		toolTokens = EstimateToolSchemaTokens(a.Registry.GetAvailableTools(allowedTools))
	}

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
			return tokensFromChars(priorChars) + toolTokens, message.CompletionTokens, true
		}
	}

	if len(messages) > 0 {
		totalChars := 0
		for _, m := range messages {
			totalChars += messageChars(m)
		}
		estPrompt := tokensFromChars(totalChars) + toolTokens
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
