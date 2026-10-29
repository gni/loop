package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
)

func testRequestConfig(endpoint string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Endpoint = endpoint
	cfg.Model = "local"
	cfg.ContextWindowLimit = 8192
	cfg.MinContextWindow = 8192
	cfg.AutoAdaptContext = false
	cfg.ReasoningEffort = "off"
	return cfg
}

func TestMinimalWireRequestDoesNotGuessUnsupportedFields(t *testing.T) {
	cfg := testRequestConfig("http://localhost:1234/v1")
	p := &OpenAICompatibleProvider{Config: cfg}
	_, _, body, err := p.prepareChatCompletionRequest([]db.Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens", "temperature", "reasoning_effort", "reasoning_control", "thinking_budget_tokens", "chat_template_kwargs", "parallel_tool_calls", "stream_options", "frequency_penalty"} {
		if _, ok := fields[name]; ok {
			t.Errorf("unsupported field %q leaked into request: %s", name, body)
		}
	}
	for _, name := range []string{"model", "messages", "stream"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing mandatory field %q", name)
		}
	}
}

func TestSupportedParametersAreOptedIntoByProvider(t *testing.T) {
	cfg := testRequestConfig("http://localhost:1234/v1")
	cfg.ActiveProvider = "local"
	cfg.Providers = map[string]config.ProviderConfig{"local": {
		ChatCapabilities: config.ChatCapabilities{Temperature: true, MaxTokens: true, StreamOptions: true, ParallelToolCalls: true},
	}}
	cfg.ParallelToolCalls = true
	p := &OpenAICompatibleProvider{Config: cfg}
	_, _, body, err := p.prepareChatCompletionRequest([]db.Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"temperature", "max_tokens", "stream_options"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("explicitly supported %q missing: %s", name, body)
		}
	}
	if _, ok := fields["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens must be independent from max_tokens")
	}
}

func TestFakeOpenAICompatibleSSE(t *testing.T) {
	var mu sync.Mutex
	var request map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/props") {
			_, _ = w.Write([]byte(`{"n_ctx":8192}`))
			return
		}
		if r.Method != "POST" {
			http.Error(w, "wrong method", 405)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("request decode: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := testRequestConfig(server.URL + "/v1")
	p := &OpenAICompatibleProvider{Config: cfg, HttpClient: server.Client()}
	chunks := make(chan StreamChunk, 32)
	msg, err := p.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if msg == nil || msg.Content != "Hello world" {
		t.Fatalf("unexpected stream result: %+v", msg)
	}
	if _, ok := request["max_completion_tokens"]; ok {
		t.Error("unadvertised parameter sent to local server")
	}
}

func TestNativeProtocolDoesNotExecuteToolTagsInOrdinaryText(t *testing.T) {
	cfg := testRequestConfig("http://localhost:1234/v1")
	p := &OpenAICompatibleProvider{Config: cfg}
	chunks := make(chan StreamChunk, 10)
	var dest strings.Builder
	filter := p.newStreamTextFilter(context.Background(), chunks, "text", &dest)
	literal := `example: <bash>{"command":"rm -rf ."}</bash>`
	filter.Write(literal)
	filter.Flush()
	if got := dest.String(); got != literal {
		t.Errorf("literal code was modified: %q", got)
	}
	if calls := assembleToolCalls(nil, "no calls", ""); len(calls) != 0 {
		t.Errorf("unexpected calls: %+v", calls)
	}
}

func TestContextOverflowIsRejectedBeforeSending(t *testing.T) {
	cfg := testRequestConfig("http://localhost:1234/v1")
	cfg.ContextWindowLimit = 256
	cfg.MaxCompletionTokens = 128
	p := &OpenAICompatibleProvider{Config: cfg}
	_, _, _, err := p.prepareChatCompletionRequest([]db.Message{{Role: "user", Content: strings.Repeat("x", 50000)}}, nil)
	if err == nil {
		t.Error("oversized newest message should not silently be discarded")
	}
}

func TestSSERequestCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/props") {
			_, _ = w.Write([]byte(`{"n_ctx":8192}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": connected\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := testRequestConfig(server.URL + "/v1")
	p := &OpenAICompatibleProvider{Config: cfg, HttpClient: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.StreamChatCompletions(ctx, []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 8))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("wanted context cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt stream")
	}
}

func TestToolBatchingIsIndependentOfLocalExecutionConcurrency(t *testing.T) {
	cfg := testRequestConfig("http://localhost:1234/v1")
	cfg.ParallelToolCalls = false
	cfg.ChatCapabilities.ParallelToolCalls = true
	p := &OpenAICompatibleProvider{Config: cfg}
	tdef := tool.NewFunctionTool("read", "Read a file", map[string]tool.SchemaProp{"path": tool.StringProp("File path")}, "path")
	_, _, payload, err := p.prepareChatCompletionRequest([]db.Message{{Role: "user", Content: "Read a and b"}}, []tool.Tool{tdef})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["parallel_tool_calls"] != true {
		t.Fatalf("backend must permit multi-call responses independently of execution mode: %s", payload)
	}
}
