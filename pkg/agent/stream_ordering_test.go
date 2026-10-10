package agent

import (
	"bytes"
	"context"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// streamingProvider signals completion through the channel exactly like the real
// provider: chunks first, then the message and error together.
type orderingProvider struct {
	response *db.Message
}

func (p *orderingProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	emitChunk(ctx, chunkChan, StreamChunk{Type: "text", Content: "streamed"})
	return p.response, nil
}

func (p *orderingProvider) CheckThinkingSupport(context.Context) bool { return false }

// Defect #1: the stream goroutine must publish assistantMsg before signalling the
// error, so the receiver (which reads assistantMsg after <-streamErrChan) sees the
// message for a successful turn. Under -race the old ordering reports a data race.
func TestSuccessfulStreamMessageIsVisibleAfterErrSignal(t *testing.T) {
	a := &Agent{
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    5,
		},
		LLMProvider: &orderingProvider{response: &db.Message{
			Role:             "assistant",
			Content:          "the answer",
			PromptTokens:     10,
			CompletionTokens: 5,
		}},
		Registry: tool.NewToolRegistry(),
	}

	messages := []db.Message{{Role: "system", Content: "system"}}
	a.RunAgentLoop(context.Background(), &bytes.Buffer{}, &messages, "prompt", nil, style.UITheme{}, true, "")

	last := messages[len(messages)-1]
	if last.Role != "assistant" || last.Content != "the answer" {
		t.Fatalf("receiver observed %q (%d messages), want the streamed assistant message", last.Content, len(messages))
	}
}
