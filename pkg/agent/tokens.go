package agent

import (
	"encoding/json"
	"math"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
)

const charsPerToken = 3.2

func TokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	tokens := int(math.Round(float64(chars) / charsPerToken))
	if tokens == 0 {
		return 1
	}
	return tokens
}

func EstimateToolSchemaTokens(tools []tool.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return TokensFromChars(len(data))
}

func messageChars(m db.Message) int {
	chars := len(m.Content) + len(m.ReasoningContent)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	for _, block := range m.ContentBlocks {
		if block.Type == "image" {
			chars += 4096
		} else {
			chars += len(block.Text)
		}
	}
	return chars
}

func EstimateMessageTokens(m db.Message) int {
	// Provider usage describes an entire request, not the size of this message.
	// Compaction must budget the content actually retained in the history.
	return TokensFromChars(messageChars(m) + len(m.Role) + len(m.Name) + len(m.ToolCallID) + 12)
}

func EstimateMessagesTokens(messages []db.Message) int {
	// Summarization replaces older history with new text but retains historical
	// billing totals separately. Old provider prompt counts no longer describe
	// the compacted request, so estimate from retained content instead.
	for _, m := range messages {
		if (m.Role == "system" || m.Role == "user") && m.Usage != nil {
			chars := 0
			for _, retained := range messages {
				chars += messageChars(retained) + len(retained.Role) + len(retained.Name) + len(retained.ToolCallID) + 12
			}
			return TokensFromChars(chars)
		}
	}
	trailingTokens := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].PromptTokens > 0 {
			return messages[i].PromptTokens + messages[i].CompletionTokens + trailingTokens
		}
		trailingTokens += TokensFromChars(messageChars(messages[i]))
	}
	return trailingTokens
}

func CacheTokenTotals(history []db.Message) (int, int) {
	var reads, writes int
	for _, m := range history {
		if m.Usage != nil {
			reads += m.Usage.CacheRead
			writes += m.Usage.CacheWrite
			continue
		}
		reads += m.CacheReadTokens
		writes += m.CacheWriteTokens
	}
	return reads, writes
}

var DefaultContextTiers = []int{32768, 65536, 131072, 262144, 524288, 1048576}

func GetAdaptiveContextLimit(currentTokens int, serverLimit int, configuredLimit int, minWindow int, autoAdapt bool) int {
	maxCeiling := configuredLimit
	if maxCeiling <= 0 {
		maxCeiling = 128000
	}
	if serverLimit > 0 && (serverLimit < maxCeiling || configuredLimit == 128000) {
		maxCeiling = serverLimit
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

	neededTokens := currentTokens + 4096

	tiers := []int{minWindow}
	for _, t := range DefaultContextTiers {
		if t > minWindow && t <= maxCeiling {
			tiers = append(tiers, t)
		}
	}
	if tiers[len(tiers)-1] < maxCeiling {
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

func ResolveAdaptiveContextLimit(cfg *config.Config, serverLimit, currentTokens int) int {
	autoAdapt := true
	minWindow := 32768
	configuredLimit := 128000
	if cfg != nil {
		autoAdapt = cfg.AutoAdaptContext
		if cfg.MinContextWindow > 0 {
			minWindow = cfg.MinContextWindow
		}
		if cfg.ContextWindowLimit > 0 {
			configuredLimit = cfg.ContextWindowLimit
		}
	}
	return GetAdaptiveContextLimit(currentTokens, serverLimit, configuredLimit, minWindow, autoAdapt)
}

func CalculateHistoryTokens(history []db.Message) (int, int) {
	var totalPrompt, totalCompletion, chars int
	for _, m := range history {
		if m.Usage != nil {
			totalPrompt += m.Usage.Input
			totalCompletion += m.Usage.Output
			chars += messageChars(m)
			continue
		}
		if m.Role == "assistant" && (messageChars(m) > 0) {
			prompt := m.PromptTokens
			if prompt == 0 {
				prompt = TokensFromChars(chars)
			}
			totalPrompt += prompt
			totalCompletion += m.CompletionTokens
		}
		chars += messageChars(m)
	}
	return totalPrompt, totalCompletion
}

func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	prompt, completion, _ := a.GetGlobalTokenUsage(messages, allowedTools)
	return prompt, completion
}

func (a *Agent) GetGlobalTokenUsage(messages []db.Message, allowedTools []string) (int, int, bool) {
	// Estimate the retained next-request input, not past billing totals.
	// Reasoning tokens and evicted history are not resubmitted to the provider.
	if a != nil {
		messages = inferenceMessages(a.Context(), messages)
		if a.Config == nil || a.Config.AutoAdaptContext {
			messages = CompactHistoricalToolOutputs(messages)
		}
	} else {
		messages = compactRuntimeMessages(messages)
	}
	messages, _ = sanitizeRequestMessages(messages)
	messages = EnforceToolPairingInvariance(messages)

	chars := 0
	for _, m := range messages {
		chars += messageChars(m) + len(m.Role) + len(m.Name) + len(m.ToolCallID) + 12
	}

	toolTokens := 0
	if a != nil && a.Registry != nil {
		tools := a.inferenceTools(a.Context(), allowedTools)
		if a.Config != nil {
			tools = prepareToolDefinitions(tools, a.Config.CompactPrompt)
			if strings.EqualFold(strings.TrimSpace(a.Config.ToolProtocol), "xml") {
				tools = nil
			}
		}
		toolTokens = EstimateToolSchemaTokens(tools)
	}

	if estimate := TokensFromChars(chars) + toolTokens; estimate > 0 {
		return estimate, 0, true
	}
	return 0, 0, false
}

func (a *Agent) GetSessionTotalCompletionTokens(messages []db.Message) int {
	_, totalCompletion := CalculateHistoryTokens(messages)
	return totalCompletion
}

func (a *Agent) GetLatestAssistantCompletionTokens(messages []db.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "assistant" && m.CompletionTokens > 0 {
			return m.CompletionTokens
		}
	}
	return 0
}

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

	return ResolveAdaptiveContextLimit(a.Config, serverLimit, currentPromptTokens)
}
