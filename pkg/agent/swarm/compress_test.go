package swarm

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type compressMockProvider struct {
	responses []*db.Message
	callIndex int
}

func (p *compressMockProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []agent.Tool,
	chunkChan chan<- agent.StreamChunk,
) (*db.Message, error) {
	if p.callIndex < len(p.responses) {
		resp := p.responses[p.callIndex]
		p.callIndex++
		return resp, nil
	}
	return &db.Message{Role: "assistant", Content: "Done"}, nil
}

func (p *compressMockProvider) CheckThinkingSupport(ctx context.Context) bool { return false }

func TestSubagentCompressesWhenApproachingContextLimit(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{
			ContextWindowLimit:   32768,
			AutoAdaptContext:     false,
			CompressionThreshold: 0.80,
		},
		LLMProvider: &compressMockProvider{responses: []*db.Message{
			{Role: "assistant", Content: "compressed summary"},
		}},
	}

	ma := &MultiAgent{
		Name:      "tester",
		BaseAgent: a,
	}

	// System prompt + enough history to cross 0.80 * 32768 tokens (~26214).
	// 100k chars / 4 = 25k prompt tokens.
	ma.History = []db.Message{{Role: "system", Content: "system prompt"}}
	for i := 0; i < 4; i++ {
		ma.History = append(ma.History, db.Message{Role: "user", Content: strings.Repeat("x", 10000)})
		ma.History = append(ma.History, db.Message{Role: "assistant", Content: strings.Repeat("y", 15000)})
		ma.History = append(ma.History, db.Message{Role: "tool", Name: "read", Content: strings.Repeat("z", 15000)})
	}

	before := len(ma.History)
	var out strings.Builder
	ma.compressIfNeeded(context.Background(), &out, style.UITheme{})

	if len(ma.History) >= before {
		t.Fatalf("expected compression to shrink history: before=%d after=%d", before, len(ma.History))
	}
	if ma.History[0].Role != "system" {
		t.Fatalf("system prompt must be preserved at index 0, got role %q", ma.History[0].Role)
	}
	if !strings.Contains(ma.History[1].Content, "compressed summary") {
		t.Fatalf("expected summary message at index 1, got: %q", ma.History[1].Content)
	}
	if !strings.Contains(out.String(), "compressing older conversation history") {
		t.Fatalf("expected compression notice on writer, got: %q", out.String())
	}
}

func TestSubagentDoesNotCompressBelowThreshold(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{ContextWindowLimit: 128000, AutoAdaptContext: false},
		LLMProvider: &compressMockProvider{responses: []*db.Message{
			{Role: "assistant", Content: "should not be called"},
		}},
	}
	ma := &MultiAgent{Name: "tester", BaseAgent: a}
	ma.History = []db.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "small task"},
		{Role: "assistant", Content: "answer"},
		{Role: "tool", Name: "read", Content: "output"},
	}
	before := len(ma.History)
	ma.compressIfNeeded(context.Background(), io.Discard, style.UITheme{})
	if len(ma.History) != before {
		t.Fatalf("history must be untouched below threshold: before=%d after=%d", before, len(ma.History))
	}
}

func TestSubagentToolOutputIsPruned(t *testing.T) {
	a := &agent.Agent{Config: &config.Config{MaxToolOutputBytes: 128}, WorkspaceRoot: t.TempDir()}
	ma := &MultiAgent{Name: "tester", BaseAgent: a}

	output := strings.Repeat("q", 2000)
	pruned := ma.pruneToolOutput("call_1", "bash", output)
	if len(pruned) >= len(output) {
		t.Fatalf("expected pruned output shorter than original: %d vs %d", len(pruned), len(output))
	}
	if !strings.Contains(pruned, "spill") {
		t.Fatalf("expected spill-to-disk guidance in pruned output, got: %q", pruned)
	}

	// Output below the threshold must pass through untouched.
	small := "short output"
	if got := ma.pruneToolOutput("call_2", "bash", small); got != small {
		t.Fatalf("small output must be unchanged, got: %q", got)
	}
}

// blockingCompressProvider releases the summary response only after the test signals,
// so the caller can append to history "during" the network call.
type blockingCompressProvider struct {
	response *db.Message
	release  chan struct{}
	arrived  chan struct{}
}

func (p *blockingCompressProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []agent.Tool,
	chunkChan chan<- agent.StreamChunk,
) (*db.Message, error) {
	close(p.arrived)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.response, nil
}

func (p *blockingCompressProvider) CheckThinkingSupport(context.Context) bool { return false }

// Defect #2: a message appended to ma.History while the summarization network call is
// in flight must survive the swap-in, not be overwritten by the pre-call snapshot.
func TestConcurrentAppendDuringCompressionIsNotLost(t *testing.T) {
	provider := &blockingCompressProvider{
		response: &db.Message{Role: "assistant", Content: "compressed summary"},
		release:  make(chan struct{}),
		arrived:  make(chan struct{}),
	}
	a := &agent.Agent{
		Config: &config.Config{
			ContextWindowLimit:   32768,
			AutoAdaptContext:     false,
			CompressionThreshold: 0.80,
		},
		LLMProvider: provider,
	}

	ma := &MultiAgent{Name: "tester", BaseAgent: a}
	ma.History = []db.Message{{Role: "system", Content: "system prompt"}}
	for i := 0; i < 4; i++ {
		ma.History = append(ma.History, db.Message{Role: "user", Content: strings.Repeat("x", 10000)})
		ma.History = append(ma.History, db.Message{Role: "assistant", Content: strings.Repeat("y", 15000)})
		ma.History = append(ma.History, db.Message{Role: "tool", Name: "read", Content: strings.Repeat("z", 15000)})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ma.compressIfNeeded(context.Background(), io.Discard, style.UITheme{})
	}()

	select {
	case <-provider.arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("compression call never started")
	}

	// Concurrent append, exactly as Start() does.
	ma.HistoryMu.Lock()
	ma.History = append(ma.History, db.Message{Role: "user", Content: "arrived during compression"})
	ma.HistoryMu.Unlock()

	close(provider.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("compression did not finish")
	}

	found := false
	for _, m := range ma.History {
		if m.Content == "arrived during compression" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("concurrent append lost: history=%d messages, none carries the delta", len(ma.History))
	}
	if !strings.Contains(ma.History[1].Content, "compressed summary") {
		t.Fatalf("compressed history missing summary: %q", ma.History[1].Content)
	}
}
