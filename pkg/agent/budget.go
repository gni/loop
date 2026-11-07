package agent

import (
	"context"
	"fmt"
	"math"

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
	return p.budgetRequestContext(nil, messages, tools)
}

func (p *OpenAICompatibleProvider) budgetRequestContext(ctx context.Context, messages []db.Message, tools []tool.Tool) ([]db.Message, int, error) {
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
	available := contextLimit - floor - headroom
	if available <= 0 {
		return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("reserved output exceeds model context limit %d", contextLimit)}
	}
	clean := EnforceToolPairingInvariance(messages)
	schemaTokens := EstimateToolSchemaTokens(tools)
	// A measured request may have a different token density than the fallback.
	// Retain the conservative ratio if the server becomes unavailable mid-trim.
	ratio := 1.0
	used := 0
	measured := false
	for attempt := 0; attempt < 4; attempt++ {
		if ctx != nil && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		approximate := requestTokens(clean) + schemaTokens
		used, measured = p.countRequestTokens(ctx, clean, tools, output)
		if measured {
			ratio = max(ratio, float64(used)/float64(max(1, approximate)))
		} else {
			used = int(math.Ceil(float64(approximate) * ratio))
		}
		if used <= available {
			return clean, min(output, contextLimit-used-headroom), nil
		}
		if attempt == 3 {
			break
		}
		target := available - schemaTokens
		if measured || ratio > 1 {
			// Leave a little room for the changed template when pruning old units.
			target = int(float64(available)*float64(approximate)/float64(max(1, used))*0.9) - schemaTokens
		}
		trimmed := EnforceToolPairingInvariance(trimMessagesToTokenBudget(clean, max(0, target)))
		if len(trimmed) == len(clean) {
			break
		}
		clean = trimmed
	}
	qualifier := "approximately "
	if measured {
		qualifier = ""
	}
	return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("task instructions and latest evidence require %s%d tokens plus %d reserved output; context limit is %d", qualifier, used, floor, contextLimit)}
}
