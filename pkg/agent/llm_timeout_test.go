package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loop/pkg/config"
	"loop/pkg/db"
)

func TestLLMStreamChatCompletionsTimeout(t *testing.T) {
	done := make(chan struct{})
	defer close(done)

	// Server hangs without writing response headers
	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		select {
		case <-done:
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	}))
	defer hangServer.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = hangServer.URL
	cfg.Timeout = 1 // 1 second timeout

	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             hangServer.Client(),
		ThinkingSupportChecked: true,
	}

	start := time.Now()
	chunkChan := make(chan StreamChunk, 10)
	_, err := provider.StreamChatCompletions(
		context.Background(),
		[]db.Message{{Role: "user", Content: "hello"}},
		nil,
		chunkChan,
	)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "timed out after 1 seconds") && !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("unexpected error message: %v", err)
	}

	if duration > 3*time.Second {
		t.Fatalf("timeout took too long: %v (expected ~1s)", duration)
	}
}

func TestLLMStreamChatCompletionsStreamingTimeout(t *testing.T) {
	done := make(chan struct{})
	defer close(done)

	// Server sends headers and one chunk, then hangs
	hangMidStreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first chunk \"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	}))
	defer hangMidStreamServer.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = hangMidStreamServer.URL
	cfg.Timeout = 1 // 1 second timeout

	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             hangMidStreamServer.Client(),
		ThinkingSupportChecked: true,
	}

	start := time.Now()
	chunkChan := make(chan StreamChunk, 10)
	partialMsg, err := provider.StreamChatCompletions(
		context.Background(),
		[]db.Message{{Role: "user", Content: "hello"}},
		nil,
		chunkChan,
	)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("expected streaming timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "timed out after 1 seconds") && !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Partial message should still contain the content received before timeout
	if partialMsg == nil || !strings.Contains(partialMsg.Content, "first chunk") {
		t.Fatalf("expected partial content 'first chunk', got: %#v", partialMsg)
	}

	if duration > 3*time.Second {
		t.Fatalf("streaming timeout took too long: %v (expected ~1s)", duration)
	}
}

func TestLLMStreamChatCompletionsResetsTimeoutOnActivity(t *testing.T) {
	done := make(chan struct{})
	defer close(done)

	// Server streams multiple chunks with delays between them.
	// Total duration is ~1500ms, exceeding the 1000ms configured timeout.
	// Because chunks arrive every 300ms, the inactivity timer is refreshed
	// and the stream should complete successfully without timing out.
	activeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		for i := 0; i < 5; i++ {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk%d \"}}]}\n\n", i)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer activeServer.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = activeServer.URL
	cfg.Timeout = 1 // 1 second timeout

	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             activeServer.Client(),
		ThinkingSupportChecked: true,
	}

	chunkChan := make(chan StreamChunk, 20)
	msg, err := provider.StreamChatCompletions(
		context.Background(),
		[]db.Message{{Role: "user", Content: "hello"}},
		nil,
		chunkChan,
	)

	if err != nil {
		t.Fatalf("expected active stream to succeed, got error: %v", err)
	}

	if msg == nil || !strings.Contains(msg.Content, "chunk0 chunk1 chunk2 chunk3 chunk4") {
		t.Fatalf("expected complete content across all chunks, got: %#v", msg)
	}
}

func TestCheckThinkingSupportTimeout(t *testing.T) {
	done := make(chan struct{})
	defer close(done)

	hangServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
	}))
	defer hangServer.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = hangServer.URL
	cfg.Timeout = 1

	provider := &OpenAICompatibleProvider{
		Config:     cfg,
		HttpClient: hangServer.Client(),
	}

	start := time.Now()
	supported := provider.CheckThinkingSupport(context.Background())
	duration := time.Since(start)

	if supported {
		t.Fatal("expected CheckThinkingSupport to return false on timeout")
	}

	if duration > 3*time.Second {
		t.Fatalf("CheckThinkingSupport took too long: %v", duration)
	}
}

func TestLLMFiltersOutErrorRoleFromHistory(t *testing.T) {
	var receivedRoles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			for _, m := range req.Messages {
				receivedRoles = append(receivedRoles, m.Role)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = server.URL
	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             server.Client(),
		ThinkingSupportChecked: true,
	}

	messages := []db.Message{
		{Role: "user", Content: "hi"},
		{Role: "error", Content: "HTTP request failed: dial tcp 127.0.0.1:8080: connect: connection refused"},
		{Role: "user", Content: "hi"},
	}

	chunkChan := make(chan StreamChunk, 10)
	msg, err := provider.StreamChatCompletions(context.Background(), messages, nil, chunkChan)
	if err != nil {
		t.Fatalf("StreamChatCompletions failed: %v", err)
	}
	if msg == nil || msg.Content != "ok" {
		t.Fatalf("expected msg ok, got: %#v", msg)
	}

	for _, role := range receivedRoles {
		if role == "error" {
			t.Fatalf("LLM received invalid role 'error' in payload: %v", receivedRoles)
		}
	}
	if len(receivedRoles) != 1 || receivedRoles[0] != "user" {
		t.Fatalf("expected single merged user role, got: %v", receivedRoles)
	}
}

func TestLLMEmptyStreamReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Empty body - closes immediately without chunks
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Endpoint = server.URL
	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             server.Client(),
		ThinkingSupportChecked: true,
	}

	chunkChan := make(chan StreamChunk, 10)
	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, chunkChan)
	if err == nil {
		t.Fatal("expected error on empty stream, got nil")
	}
	if !strings.Contains(err.Error(), "without returning any content") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLLMNonRetryableConnectionErrorFailsFast(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Endpoint = "http://127.0.0.1:54321" // unlistened port -> ECONNREFUSED
	cfg.Timeout = 120

	provider := &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             http.DefaultClient,
		ThinkingSupportChecked: true,
	}

	start := time.Now()
	chunkChan := make(chan StreamChunk, 10)
	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, chunkChan)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("expected connection refused error, got nil")
	}
	if duration > 2*time.Second {
		t.Fatalf("expected fast failure without 3 retries, took: %v", duration)
	}
}
