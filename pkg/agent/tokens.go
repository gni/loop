package agent

import (
	"encoding/json"
	"math"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
)

const (
	charsPerToken        = 2.5 // conservative ratio for overflow budgeting
	displayCharsPerToken = 3.8 // realistic BPE average for status bar estimates
)

func TokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	tokens := int(math.Ceil(float64(chars) / charsPerToken))
	if tokens == 0 {
		return 1
	}
	return tokens
}

func displayTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	tokens := int(math.Ceil(float64(chars) / displayCharsPerToken))
	if tokens == 0 {
		return 1
	}
	return tokens
}

func estimateDisplayToolSchemaTokens(tools []tool.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return displayTokensFromChars(len(data)) + 4*len(tools)
}

func estimateDisplayMessageTokens(m db.Message) int {
	// Reasoning content is never resubmitted to the provider on subsequent turns.
	chars := len(m.Content) + len(m.Role) + len(m.Name) + len(m.ToolCallID)
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
	return displayTokensFromChars(chars) + 4
}

func EstimateToolSchemaTokens(tools []tool.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return TokensFromChars(len(data)) + 8*len(tools)
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
	return TokensFromChars(messageChars(m)+len(m.Role)+len(m.Name)+len(m.ToolCallID)) + 8
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

var DefaultContextTiers = []int{8192, 16384, 32768, 65536, 131072, 262144, 524288, 1048576}

func GetAdaptiveContextLimit(currentTokens int, serverLimit int, configuredLimit int, minWindow int, autoAdapt bool) int {
	maxCeiling := configuredLimit
	if maxCeiling <= 0 {
		maxCeiling = config.DefaultContextWindowLimit
	}
	if serverLimit > 0 {
		maxCeiling = min(maxCeiling, serverLimit)
	}

	if !autoAdapt {
		return maxCeiling
	}

	if minWindow <= 0 {
		minWindow = config.DefaultMinContextWindow
	}
	if minWindow > maxCeiling {
		minWindow = maxCeiling
	}

	reserve := min(4096, max(1, maxCeiling/4))
	neededTokens := min(currentTokens, maxCeiling) + reserve

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
	minWindow := config.DefaultMinContextWindow
	configuredLimit := config.DefaultContextWindowLimit
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
	// Prefer exact provider-measured prompt tokens from the latest completed
	// assistant turn (when no post-turn compaction summary invalidated it),
	// adding a calibrated estimate only for unsent messages appended afterward.
	var cfg *config.Config
	if a != nil {
		a.LLMProviderMu.RLock()
		cfg = a.Config
		a.LLMProviderMu.RUnlock()
		messages = inferenceMessages(a.Context(), messages)
		if historyCompressionEnabled(cfg) {
			messages = CompactHistoricalToolOutputs(messages)
		}
	} else {
		messages = compactRuntimeMessages(messages)
	}
	messages, _ = sanitizeRequestMessages(messages)
	messages = EnforceToolPairingInvariance(messages)

	hasCompactionAnchor := false
	lastMeasured := -1
	for i, msg := range messages {
		if (msg.Role == "system" || msg.Role == "user") && msg.Usage != nil {
			hasCompactionAnchor = true
		}
		if msg.Role == "assistant" && msg.PromptTokens > 0 {
			lastMeasured = i
		}
	}
	if lastMeasured >= 0 && !hasCompactionAnchor {
		base := messages[lastMeasured].PromptTokens
		if lastMeasured == len(messages)-1 {
			return base, 0, false
		}
		delta := estimateDisplayMessageTokens(messages[lastMeasured])
		for _, msg := range messages[lastMeasured+1:] {
			delta += estimateDisplayMessageTokens(msg)
		}
		return base + delta, 0, true
	}

	messageTokens := 0
	for _, msg := range messages {
		messageTokens += estimateDisplayMessageTokens(msg)
	}

	toolTokens := 0
	if a != nil && a.Registry != nil {
		tools := a.inferenceTools(a.Context(), allowedTools)
		if cfg != nil {
			tools = prepareToolDefinitions(tools, cfg.CompactPrompt)
			if strings.EqualFold(strings.TrimSpace(cfg.ToolProtocol), "xml") {
				tools = nil
			}
		}
		toolTokens = estimateDisplayToolSchemaTokens(tools)
	}

	if estimate := messageTokens + toolTokens; estimate > 0 {
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
		return config.DefaultContextWindowLimit
	}
	serverLimit := 0
	a.LLMProviderMu.RLock()
	if detector, ok := a.LLMProvider.(ContextLimitDetector); ok {
		serverLimit = detector.GetDetectedContextLimit()
	}
	if provider, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok {
		if limit := provider.selectedModel().ContextWindow; limit > 0 {
			if serverLimit <= 0 || limit < serverLimit {
				serverLimit = limit
			}
		}
	}
	limit := ResolveAdaptiveContextLimit(a.Config, serverLimit, currentPromptTokens)
	a.LLMProviderMu.RUnlock()

	return limit
}
