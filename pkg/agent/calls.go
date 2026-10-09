package agent

import (
	"context"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	transporthttp "loop/pkg/transport/http"
)

var streamSendTimeout = 500 * time.Millisecond

// emitChunk sends a chunk without ever blocking forever.
func emitChunk(ctx context.Context, chunkChan chan<- StreamChunk, chunk StreamChunk) {
	select {
	case chunkChan <- chunk:
		return
	default:
	}
	timer := time.NewTimer(streamSendTimeout)
	defer timer.Stop()
	select {
	case chunkChan <- chunk:
	case <-ctx.Done():
	case <-timer.C:
	}
}

func isNonRetryableError(err error) bool {
	return transporthttp.IsNonRetryableError(err)
}

func assembleToolCalls(toolCallsMap map[int]*db.ToolCall, rawText string, rawReasoning string, parallelAllowed bool) []db.ToolCall {
	var calls []db.ToolCall
	if len(toolCallsMap) > 0 {
		maxIdx := -1
		for idx := range toolCallsMap {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if tc, ok := toolCallsMap[i]; ok {
				cleaned := *tc
				cleaned.Function.Name = tool.NormalizeName(cleaned.Function.Name)
				cleaned.Function.Arguments = SanitizeLLMControlTokens(cleaned.Function.Arguments)
				calls = append(calls, cleaned)
			}
		}
	} else {
		calls = ParseFallbackToolCalls(rawText)
		if rawReasoning != "" {
			reasoningCalls := ParseFallbackToolCalls(rawReasoning)
			if len(calls) == 0 {
				calls = reasoningCalls
			} else {
				for _, rc := range reasoningCalls {
					duplicate := false
					for _, c := range calls {
						if c.Function.Name == rc.Function.Name && c.Function.Arguments == rc.Function.Arguments {
							duplicate = true
							break
						}
					}
					if !duplicate {
						calls = append(calls, rc)
					}
				}
			}
		}
	}
	if !parallelAllowed && len(calls) > 1 {
		calls = calls[:1]
	}
	return calls
}
