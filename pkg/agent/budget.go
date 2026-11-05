package agent

import (
	"fmt"
	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
)

func messageTokenChars(msg db.Message) int {
	return messageChars(msg) + len(msg.Role) + len(msg.Name) + len(msg.ToolCallID) + 12
}

func requestTokens(messages []db.Message) int {
	chars := 0
	for _, msg := range messages {
		chars += messageTokenChars(msg)
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
	// JSON framing, tool wrappers, and tokenizer differences are not captured
	// by the character-based fallback estimate. Preserve a small safety margin.
	headroom := max(64, contextLimit/40)
	available := contextLimit - EstimateToolSchemaTokens(tools) - floor - headroom
	if available <= 0 {
		return nil, 0, fmt.Errorf("tool definitions and reserved output exceed model context limit %d", contextLimit)
	}
	clean := EnforceToolPairingInvariance(messages)
	if requestTokens(clean) > available {
		clean = trimMessagesToTokenBudget(clean, available)
		clean = EnforceToolPairingInvariance(clean)
	}
	used := requestTokens(clean) + EstimateToolSchemaTokens(tools)
	if used+floor+headroom > contextLimit {
		return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("task instructions and latest evidence require approximately %d tokens plus %d reserved output; context limit is %d", used, floor, contextLimit)}
	}
	return clean, min(output, contextLimit-used-headroom), nil
}
