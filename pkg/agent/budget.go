package agent

import (
	"fmt"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
)

func requestTokens(messages []db.Message) int {
	chars := 0
	for _, msg := range messages {
		chars += messageChars(msg) + len(msg.Role) + len(msg.Name) + len(msg.ToolCallID) + 12
	}
	return TokensFromChars(chars)
}

func (p *OpenAICompatibleProvider) budgetRequest(messages []db.Message, tools []tool.Tool) ([]db.Message, int, error) {
	contextLimit := ResolveAdaptiveContextLimit(p.Config, p.GetDetectedContextLimit(), requestTokens(messages)+EstimateToolSchemaTokens(tools))
	selected := p.selectedModel()
	if selected.ContextWindow > 0 {
		contextLimit = min(contextLimit, selected.ContextWindow)
	}
	output := limits.DefaultMaxCompletionTokens
	if p.Config != nil && p.Config.MaxCompletionTokens > 0 {
		output = p.Config.MaxCompletionTokens
	}
	if selected.MaxOutput > 0 {
		output = min(output, selected.MaxOutput)
	}
	floor := min(output, limits.For(p).CompletionTokenFloor)
	available := contextLimit - EstimateToolSchemaTokens(tools) - floor
	if available <= 0 {
		return nil, 0, fmt.Errorf("tool definitions and reserved output exceed model context limit %d", contextLimit)
	}
	clean := EnforceToolPairingInvariance(messages)
	if requestTokens(clean) > available {
		totalChars := 0
		for _, msg := range clean {
			totalChars += messageChars(msg)
		}
		clean = trimMessagesToCharBudget(clean, totalChars, int(float64(max(0, available-len(clean)*5))*charsPerToken))
		clean = EnforceToolPairingInvariance(clean)
	}
	used := requestTokens(clean) + EstimateToolSchemaTokens(tools)
	if used+floor > contextLimit {
		return nil, 0, fmt.Errorf("current input requires approximately %d tokens plus %d reserved output; context limit is %d", used, floor, contextLimit)
	}
	return clean, min(output, contextLimit-used), nil
}
