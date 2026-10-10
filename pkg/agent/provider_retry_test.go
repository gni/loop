package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loop/pkg/config"
	"loop/pkg/db"
)

// newTestProvider wires a provider to a test server with the capability probe already
// answered, so StreamChatCompletions exercises the consolidated retry path only.
func newTestProvider(cfg *config.Config, url string) *OpenAICompatibleProvider {
	cfg.Endpoint = url
	return &OpenAICompatibleProvider{
		Config:                 cfg,
		HttpClient:             http.DefaultClient,
		ThinkingSupportChecked: true,
	}
}

func TestStreamChatCompletionsRetries5xxAndReportsAllAttempts(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "upstream boom")
	}))
	defer server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, server.URL)

	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected error after retrying 5xx responses")
	}
	if !strings.Contains(err.Error(), "after 3 retries") {
		t.Fatalf("expected retry exhaustion message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream boom") {
		t.Fatalf("expected error body in message, got: %v", err)
	}
	// Initial attempt + 3 retries.
	if got := atomic.LoadInt32(&attempts); got != 4 {
		t.Fatalf("expected 4 attempts, got %d", got)
	}
}

func TestStreamChatCompletionsNonRetryableStatusReturnsImmediately(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "invalid api key")
	}))
	defer server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, server.URL)

	start := time.Now()
	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected error for non-retryable status")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected single attempt for 4xx, got %d", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("4xx should not be retried, took %v", time.Since(start))
	}
}

func TestStreamChatCompletionsCapsErrorBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		// Far larger than the 4096-byte cap.
		w.Write([]byte(strings.Repeat("x", 20000)))
	}))
	defer server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, server.URL)

	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	body := err.Error()
	if len(body) > 4200 {
		t.Fatalf("error body not capped: %d bytes", len(body))
	}
	if !strings.Contains(body, "400") {
		t.Fatalf("expected status code in error, got: %s", body)
	}
}

func TestStreamChatCompletionsEmptyErrorBodyIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, server.URL)

	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected error for empty error body")
	}
	if !strings.Contains(err.Error(), "(empty)") {
		t.Fatalf("expected empty-body marker, got: %v", err)
	}
}

func TestStreamChatCompletionsTransportErrorCarriesEndpointHint(t *testing.T) {
	// Closed listener: connection refused is transient, so the shared resilient client
	// retries it. DescribeError must keep the endpoint hint the old inline loop added.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, url)

	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected connection refused error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("Check your endpoint (%s)", url)) {
		t.Fatalf("expected endpoint hint in error, got: %v", err)
	}
}

func TestStreamChatCompletionsWatchdogTimeoutDuringBackoff(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// The watchdog fires during the first backoff sleep, so BeforeAttempt must classify
	// the failure as a timeout rather than a plain transport error.
	cfg := &config.Config{Model: "test", Timeout: 1}
	provider := newTestProvider(cfg, server.URL)

	start := time.Now()
	_, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, make(chan StreamChunk, 16))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out after 1 seconds") {
		t.Fatalf("expected watchdog timeout classification, got: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout should stop retries promptly, took %v", time.Since(start))
	}
	if atomic.LoadInt32(&attempts) > 2 {
		t.Fatalf("retries continued past the watchdog, attempts=%d", atomic.LoadInt32(&attempts))
	}
}

func TestStreamChatCompletionsSuccessfulRetryAfterTransient5xx(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.WriteHeader(http.StatusOK)
			return
		}
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{Model: "test", Timeout: 0}
	provider := newTestProvider(cfg, server.URL)

	chunkChan := make(chan StreamChunk, 16)
	msg, err := provider.StreamChatCompletions(context.Background(), []db.Message{{Role: "user", Content: "hi"}}, nil, chunkChan)
	if err != nil {
		t.Fatalf("expected success after transient failures: %v", err)
	}
	if msg == nil || msg.Content != "ok" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}
