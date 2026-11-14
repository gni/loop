package agent

import (
	"context"
	"fmt"
	"math"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
)

func requestTokens(messages []db.Message) int {
	tokens := 0
	for _, msg := range messages {
		tokens += EstimateMessageTokens(msg)
	}
	return tokens
}

func (p *OpenAICompatibleProvider) budgetRequest(messages []db.Message, tools []tool.Tool) ([]db.Message, int, error) {
	return p.budgetRequestContext(nil, messages, tools)
}

func (p *OpenAICompatibleProvider) budgetRequestContext(ctx context.Context, messages []db.Message, tools []tool.Tool) ([]db.Message, int, error) {
	schemaTokens := EstimateToolSchemaTokens(tools)
	contextLimit := ResolveAdaptiveContextLimit(p.Config, p.GetDetectedContextLimit(), requestTokens(messages)+schemaTokens)
	taskLimit := run.RequestTokenLimit(ctx)
	if taskLimit < 0 {
		return nil, 0, run.ErrTokenBudget
	}
	if taskLimit > 0 {
		contextLimit = min(contextLimit, taskLimit)
	}
	selected := p.selectedModel()
	if selected.ContextWindow > 0 {
		contextLimit = min(contextLimit, selected.ContextWindow)
	}
	output := config.DefaultMaxCompletionTokens
	if p.Config != nil && p.Config.MaxCompletionTokens > 0 {
		output = p.Config.MaxCompletionTokens
	}
	if selected.MaxOutput > 0 {
		output = min(output, selected.MaxOutput)
	}
	output = min(output, max(1, contextLimit/4))
	floor := min(output, limits.For(p).CompletionTokenFloor, max(1, contextLimit/16))
	// JSON framing, tool wrappers, and tokenizer differences are not captured
	// by the character-based fallback estimate. Preserve a small safety margin.
	headroom := max(64, contextLimit/20)
	available := contextLimit - floor - headroom
	if available <= 0 {
		if taskLimit > 0 {
			return nil, 0, run.ErrTokenBudget
		}
		return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("reserved output exceeds model context limit %d", contextLimit)}
	}
	clean := EnforceToolPairingInvariance(messages)
	originalCount := len(clean)
	// A measured request may have a different token density than the fallback.
	// Retain the conservative ratio if the server becomes unavailable mid-trim.
	ratio := p.requestCounter().estimateScale()
	used := 0
	measured := false
	for attempt := range 4 {
		if ctx != nil && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		approximate := requestTokens(clean) + schemaTokens
		used, measured = p.countRequestTokens(ctx, clean, tools, output)
		if measured {
			ratio = max(ratio, float64(used)/float64(max(1, approximate)))
		} else {
			scaled := math.Ceil(float64(approximate) * ratio)
			maximum := int(^uint(0) >> 1)
			if scaled >= float64(maximum) {
				used = maximum
			} else {
				used = int(scaled)
			}
		}
		budget := contextBreakdown(clean, schemaTokens)
		budget.Limit, budget.Input, budget.Headroom = contextLimit, used, headroom
		budget.Output = max(0, min(output, contextLimit-used-headroom))
		budget.Measured, budget.Removed = measured, originalCount-len(clean)
		p.setContextBudget(budget)
		preferredAvailable := contextLimit - output - headroom
		if used <= preferredAvailable {
			run.RecordRequestBudget(ctx, used, budget.Output)
			return clean, budget.Output, nil
		}
		if attempt == 3 {
			if used <= available {
				run.RecordRequestBudget(ctx, used, budget.Output)
				return clean, budget.Output, nil
			}
			break
		}
		target := preferredAvailable - schemaTokens
		if measured || ratio > 1 {
			// Leave a little room for the changed template when pruning old units.
			target = int(float64(preferredAvailable)*float64(approximate)/float64(max(1, used))*0.95) - schemaTokens
		}
		trimmed := EnforceToolPairingInvariance(trimMessagesToTokenBudget(clean, max(0, target)))
		if len(trimmed) == len(clean) {
			if used <= available {
				run.RecordRequestBudget(ctx, used, budget.Output)
				return clean, budget.Output, nil
			}
			break
		}
		clean = trimmed
	}
	qualifier := "approximately "
	if measured {
		qualifier = ""
	}
	breakdown := contextBreakdown(clean, schemaTokens)
	if taskLimit > 0 {
		return nil, 0, fmt.Errorf("%w: required input and output cannot fit the remaining %d tokens", run.ErrTokenBudget, taskLimit)
	}
	return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("required context needs %s%d input tokens plus %d output and %d safety tokens; limit %d (system %d, tools %d, task %d, evidence %d, history %d estimated)", qualifier, used, floor, headroom, contextLimit, breakdown.System, breakdown.Tools, breakdown.Task, breakdown.Evidence, breakdown.History)}
}
