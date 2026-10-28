package agent

import (
	"encoding/json"
	"math"

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
	if m.PromptTokens > 0 || m.CompletionTokens > 0 {
		return m.PromptTokens + m.CompletionTokens
	}
	return TokensFromChars(messageChars(m))
}

func EstimateMessagesTokens(messages []db.Message) int {
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
		if m.Usage!=nil{reads+=m.Usage.CacheRead;writes+=m.Usage.CacheWrite}
		reads += m.CacheReadTokens
		writes += m.CacheWriteTokens
	}
	return reads, writes
}

func EstimateFallbackTokens(promptTokens, completionTokens int, messages []db.Message, calls []db.ToolCall, reasoning, content string) (int, int) {
	if promptTokens == 0 {
		promptTokens = EstimateMessagesTokens(messages)
	}
	if completionTokens == 0 {
		completionTokens = TokensFromChars(messageChars(db.Message{Content: content, ReasoningContent: reasoning, ToolCalls: calls}))
	}
	return promptTokens, completionTokens
}

var DefaultContextTiers = []int{32768, 65536, 131072, 262144}

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
 if m.Usage!=nil{totalPrompt+=m.Usage.Input;totalCompletion+=m.Usage.Output}
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
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "assistant" || messageChars(m) == 0 {
			continue
		}
		if m.PromptTokens > 0 {
			return m.PromptTokens, m.CompletionTokens, false
		}
		chars := 0
		for _, prior := range messages[:i] {
			chars += messageChars(prior)
		}
		return TokensFromChars(chars), m.CompletionTokens, true
	}

	msgChars := 0
	for _, m := range messages {
		msgChars += messageChars(m)
	}

	toolTokens := 0
	if a != nil && a.Registry != nil {
		tools := a.Registry.GetAvailableTools(allowedTools)
		if a.Config != nil {
			tools = prepareToolDefinitions(tools, a.Config.CompactPrompt)
		}
		toolTokens = EstimateToolSchemaTokens(tools)
	}

	if initialTokens := TokensFromChars(msgChars) + toolTokens; initialTokens > 0 {
		return initialTokens, 0, true
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
