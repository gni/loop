package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestResilientClientSuccessFirstAttempt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewResilientClient(RetryConfig{
		Client: server.Client(),
	})

	resp, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("expected 'ok', got %q", string(body))
	}
}

func TestResilientClientRetryOn500(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := attempts.Add(1)
		if current < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("recovered"))
	}))
	defer server.Close()

	client := NewResilientClient(RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		Client:     server.Client(),
	})

	resp, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestResilientClientNonRetryableStatus(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	client := NewResilientClient(RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		Client:     server.Client(),
	})

	_, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if err == nil {
		t.Fatalf("expected error on 400 Bad Request, got nil")
	}

	if attempts.Load() != 1 {
		t.Fatalf("expected exactly 1 attempt on non-retryable 400, got %d", attempts.Load())
	}
}

func TestResilientClientContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewResilientClient(RetryConfig{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		Client:     server.Client(),
	})

	_, err := client.DoWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestIsNonRetryableError(t *testing.T) {
	cases := []struct {
		err      error
		expected bool
	}{
		{errors.New("dial tcp: connection refused"), false},
		{errors.New("dial tcp: i/o timeout"), false},
		{errors.New("context deadline exceeded"), false},
		{errors.New("network is unreachable"), false},
		{errors.New("lookup example.test: no such host"), true},
		{errors.New("tls: bad certificate"), true},
		{errors.New("random temporary network blip"), false},
		{nil, false},
	}

	for _, c := range cases {
		got := IsNonRetryableError(c.err)
		if got != c.expected {
			t.Errorf("IsNonRetryableError(%v) = %v, want %v", c.err, got, c.expected)
		}
	}
}
