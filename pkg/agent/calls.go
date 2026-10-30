package agent

import (
	"context"
	"sort"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

var streamSendTimeout = 500 * time.Millisecond

// emitChunk sends a chunk without ever blocking forever. It reports whether the chunk
// was delivered: a drop means UI output can be truncated while the persisted message
// still contains everything, so callers must be able to notice it.
func emitChunk(ctx context.Context, chunkChan chan<- StreamChunk, chunk StreamChunk) bool {
	select {
	case chunkChan <- chunk:
		return true
	default:
	}
	timer := time.NewTimer(streamSendTimeout)
	defer timer.Stop()
	select {
	case chunkChan <- chunk:
		return true
	case <-ctx.Done():
	case <-timer.C:
	}
	return false
}

// relayStreamChunks keeps reading provider output while the presentation goroutine
// executes an early file operation. The queue is deliberately bounded: under
// extreme backpressure, emitChunk's existing drop reporting still applies.
// All rendering remains on the caller goroutine, in original chunk order.
func relayStreamChunks(ctx context.Context, source <-chan StreamChunk) <-chan StreamChunk {
	out := make(chan StreamChunk)
	go func() {
		defer close(out)
		const maxQueuedChunks = 8192
		queue := make([]StreamChunk, 0, 256)
		for source != nil || len(queue) > 0 {
			var input <-chan StreamChunk
			if len(queue) < maxQueuedChunks {
				input = source
			}
			var output chan<- StreamChunk
			var next StreamChunk
			if len(queue) > 0 {
				output = out
				next = queue[0]
			}
			select {
			case chunk, ok := <-input:
				if !ok {
					source = nil
				} else {
					queue = append(queue, chunk)
				}
			case output <- next:
				queue[0] = StreamChunk{}
				queue = queue[1:]
				if len(queue) == 0 {
					queue = queue[:0]
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// ChunkDropObserver, when set, is invoked for every stream chunk that could not be
// delivered to the UI consumer. The provider layer cannot reach the agent directly, so
// it is wired through this field on the provider.
type ChunkDropObserver func()

func emitChunkObserved(ctx context.Context, chunkChan chan<- StreamChunk, chunk StreamChunk, observer ChunkDropObserver) bool {
	delivered := emitChunk(ctx, chunkChan, chunk)
	if !delivered && observer != nil {
		observer()
	}
	return delivered
}

func assembleToolCalls(toolCallsMap map[int]*db.ToolCall, rawText string, rawReasoning string) []db.ToolCall {
	var calls []db.ToolCall
	if len(toolCallsMap) > 0 {
		indices := make([]int, 0, len(toolCallsMap))
		for idx := range toolCallsMap {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			cleaned := *toolCallsMap[idx]
			cleaned.Function.Name = tool.NormalizeName(cleaned.Function.Name)
			cleaned.Function.Arguments = SanitizeLLMControlTokens(cleaned.Function.Arguments)
			calls = append(calls, cleaned)
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
	return calls
}
