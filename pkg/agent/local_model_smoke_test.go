package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"loop/pkg/db"
)

// Optional live smoke test for an actual llama.cpp, Ollama, or other server
// implementing OpenAI-compatible streaming chat completions. Not run without
// an explicit endpoint and model to avoid hitting external services in CI.
func TestLiveLocalModelSmoke(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("LOOP_TEST_LOCAL_ENDPOINT"))
	model := strings.TrimSpace(os.Getenv("LOOP_TEST_LOCAL_MODEL"))
	if endpoint == "" || model == "" {
		t.Skip("set LOOP_TEST_LOCAL_ENDPOINT and LOOP_TEST_LOCAL_MODEL")
	}
	cfg := testRequestConfig(endpoint)
	cfg.Model = model
	p := &OpenAICompatibleProvider{Config: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	msg, err := p.StreamChatCompletions(ctx, []db.Message{{Role: "user", Content: "Reply with exactly OK."}}, nil, make(chan StreamChunk, 2048))
	if err != nil {
		t.Fatalf("real backend %s (%s) incompatible: %v", model, endpoint, err)
	}
	if msg == nil || strings.TrimSpace(msg.Content) == "" {
		t.Fatalf("real backend returned an empty completion: %+v", msg)
	}
}
