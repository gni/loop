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
	return requestTokensFor(messages, false)
}

func requestTokensFor(messages []db.Message, includeReasoning bool) int {
	tokens := 0
	for _, msg := range messages {
		tokens += EstimateMessageTokensWithReasoning(msg, includeReasoning)
	}
	return tokens
}

// calibratedHistoryPromptAnchor finds the latest assistant message after any
// compaction boundary whose PromptTokens came from the active model. This lets
// request budgeting stay aligned with the server's real tokenizer when the
// endpoint does not expose /chat/completions/input_tokens.
func (p *OpenAICompatibleProvider) calibratedHistoryPromptAnchor(messages []db.Message, schemaTokens int, includeReasoning bool) (measuredIndex, measuredPrompt, prefixApprox int) {
	compactionBoundary := -1
	measuredIndex = -1
	activeModel := ""
	if p != nil && p.Config != nil {
		activeModel = p.Config.Model
	}
	for i, msg := range messages {
		if (msg.Role == "system" || msg.Role == "user") && msg.Usage != nil {
			if msg.CompactionTailMessages > 0 {
				compactionBoundary = max(compactionBoundary, i+msg.CompactionTailMessages)
			} else {
				compactionBoundary = len(messages)
			}
		}
		if msg.Role == "assistant" && msg.PromptTokens > 0 && i > compactionBoundary {
			if activeModel == "" || msg.Model == "" || msg.Model == activeModel {
				measuredIndex = i
			}
		}
	}
	if measuredIndex < 0 {
		return -1, 0, 0
	}
	return measuredIndex, messages[measuredIndex].PromptTokens, requestTokensFor(messages[:measuredIndex], includeReasoning) + schemaTokens
}

func (p *OpenAICompatibleProvider) budgetRequestContext(ctx context.Context, messages []db.Message, tools []tool.Tool) ([]db.Message, int, error) {
	return p.budgetRequestContextWithFallback(ctx, messages, tools, nil)
}

func (p *OpenAICompatibleProvider) budgetRequestContextWithFallback(ctx context.Context, messages []db.Message, tools []tool.Tool, compactFallback func() []db.Message) ([]db.Message, int, error) {
	preserveReasoning := p.PreserveReasoningEnabled()
	schemaTokens := EstimateToolSchemaTokens(tools)
	clean := EnforceToolPairingInvariance(messages)
	anchorIdx, anchorPrompt, anchorApprox := p.calibratedHistoryPromptAnchor(clean, schemaTokens, preserveReasoning)
	calibratedScale := 0.0
	if anchorIdx >= 0 && anchorApprox > 0 {
		// Retain a 5% cushion above the server's observed token density.
		calibratedScale = max(0.45, min(2.0, (float64(anchorPrompt)/float64(anchorApprox))*1.05))
	}
	initialTokens := requestTokensFor(clean, preserveReasoning) + schemaTokens
	if anchorIdx >= 0 && anchorIdx < len(clean) {
		initialTokens = anchorPrompt + requestTokensFor(clean[anchorIdx:], preserveReasoning)
	}
	contextLimit := ResolveAdaptiveContextLimit(p.Config, p.GetDetectedContextLimit(), initialTokens)
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
	// Reserve only a practical output floor before mutating or evicting history.
	// Using the full MaxCompletionTokens ceiling (e.g. 32k on a 128k window)
	// here would trigger premature history compaction around 89k tokens and
	// wipe the KV cache long before auto-compaction (109k), even though
	// budget.Output already clamps max_tokens smoothly to contextLimit-used-headroom.
	preferredOutput := min(output, max(floor, min(2048, contextLimit/16)))
	preferredAvailable := contextLimit - preferredOutput - headroom
	originalCount := len(clean)
	// A measured request may have a different token density than the fallback.
	// Retain the conservative ratio if the server becomes unavailable mid-trim.
	ratio := p.requestCounter().estimateScale()
	used := 0
	measured := false
	triedCompactFallback := false
	for attempt := 0; attempt < 4; {
		if ctx != nil && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		approximate := requestTokensFor(clean, preserveReasoning) + schemaTokens
		used, measured = p.countRequestTokens(ctx, clean, tools, output)
		if measured {
			ratio = max(ratio, float64(used)/float64(max(1, approximate)))
		} else if attempt == 0 && !triedCompactFallback && anchorIdx >= 0 && anchorIdx < len(clean) {
			// Use the server's exact PromptTokens for the already-measured prefix
			// plus a conservative estimate only for unsent messages appended after it.
			used = anchorPrompt + requestTokensFor(clean[anchorIdx:], preserveReasoning)
			if calibratedScale > 0 {
				if scaled := int(math.Ceil(float64(approximate) * calibratedScale)); scaled > used && schemaTokens > anchorPrompt {
					used = scaled
				}
			}
		} else {
			scale := ratio
			if calibratedScale > 0 && ratio <= 1 {
				scale = calibratedScale
			}
			scaled := math.Ceil(float64(approximate) * scale)
			maximum := int(^uint(0) >> 1)
			if scaled >= float64(maximum) {
				used = maximum
			} else {
				used = int(scaled)
			}
		}
		budget := contextBreakdownFor(clean, schemaTokens, preserveReasoning)
		budget.Limit, budget.Input, budget.Headroom = contextLimit, used, headroom
		budget.Output = max(0, min(output, contextLimit-used-headroom))
		budget.Measured, budget.Removed = measured, originalCount-len(clean)
		p.setContextBudget(budget)
		if used <= preferredAvailable {
			run.RecordRequestBudget(ctx, used, budget.Output)
			return clean, budget.Output, nil
		}
		// Only run historical tool output compaction when context pressure
		// actually exceeds preferredAvailable, preserving KV cache prefixes
		// during normal turns while reclaiming space before evicting messages.
		if compactFallback != nil && !triedCompactFallback {
			triedCompactFallback = true
			candidate := EnforceToolPairingInvariance(compactFallback())
			if requestTokensFor(candidate, preserveReasoning) < requestTokensFor(clean, preserveReasoning) {
				clean = candidate
				continue
			}
		}
		if attempt == 3 {
			if used <= available {
				run.RecordRequestBudget(ctx, used, budget.Output)
				return clean, budget.Output, nil
			}
			break
		}
		attempt++
		target := preferredAvailable - schemaTokens
		if measured || used != approximate {
			// Leave a little room for the changed template when pruning old units.
			target = int(float64(preferredAvailable)*float64(approximate)/float64(max(1, used))*0.95) - schemaTokens
		}
		trimmed := EnforceToolPairingInvariance(trimMessagesToTokenBudgetFor(clean, max(0, target), preserveReasoning))
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
	breakdown := contextBreakdownFor(clean, schemaTokens, preserveReasoning)
	if taskLimit > 0 {
		return nil, 0, fmt.Errorf("%w: required input and output cannot fit the remaining %d tokens", run.ErrTokenBudget, taskLimit)
	}
	return nil, 0, &run.InferenceFailure{Kind: run.ContextOverflow, Cause: fmt.Errorf("required context needs %s%d input tokens plus %d output and %d safety tokens; limit %d (system %d, tools %d, task %d, evidence %d, history %d estimated)", qualifier, used, floor, headroom, contextLimit, breakdown.System, breakdown.Tools, breakdown.Task, breakdown.Evidence, breakdown.History)}
}
