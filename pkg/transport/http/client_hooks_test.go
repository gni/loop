package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// BeforeAttempt is the hook the LLM provider uses to carry its stream watchdog into the
// shared retry loop. It must run after each backoff sleep and must be able to stop the
// retries by returning an error.
func TestBeforeAttemptRunsAfterBackoffAndCanStopRetries(t *testing.T) {
	var attempts atomic.Int32
	var observed []time.Duration
	start := time.Now()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	watchdog := errors.New("stream watchdog fired")

	client := NewResilientClient(RetryConfig{
		MaxRetries: 3,
		BaseDelay:  20 * time.Millisecond,
		Client:     server.Client(),
		BeforeAttempt: func(attempt int) error {
			observed = append(observed, time.Since(start))
			if attempt >= 2 {
				return watchdog
			}
			return nil
		},
	})

	_, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if !errors.Is(err, watchdog) {
		t.Fatalf("expected BeforeAttempt error to stop retries, got %v", err)
	}
	// Attempt 0 and attempt 1 ran; attempt 2 was refused by the hook.
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 executed attempts, got %d", attempts.Load())
	}
	if len(observed) < 2 {
		t.Fatalf("expected hook to run on retries, observed %d calls", len(observed))
	}
	// The hook must run after the backoff sleep, so the second call is at least
	// BaseDelay later than the first.
	if observed[1]-observed[0] < 15*time.Millisecond {
		t.Fatalf("hook ran before the backoff sleep: %v -> %v", observed[0], observed[1])
	}
}

// DescribeError wraps transport failures with caller context (the endpoint hint).
func TestDescribeErrorWrapsTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	client := NewResilientClient(RetryConfig{
		MaxRetries: 0,
		Client:     http.DefaultClient,
		DescribeError: func(err error) error {
			return errors.New("HTTP request failed: " + err.Error() + ". Check your endpoint (" + url + ")")
		},
	})

	_, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", url, nil)
	})
	if err == nil {
		t.Fatal("expected connection refused error")
	}
	if !strings.Contains(err.Error(), "Check your endpoint ("+url+")") {
		t.Fatalf("expected endpoint hint, got %v", err)
	}
}

// Retry exhaustion must report the last error, not a generic one.
func TestRetryExhaustionReportsLastError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("late failure"))
	}))
	defer server.Close()

	client := NewResilientClient(RetryConfig{
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		Client:     server.Client(),
	})

	_, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if err == nil {
		t.Fatal("expected error after retries")
	}
	if !strings.Contains(err.Error(), "after 2 retries") || !strings.Contains(err.Error(), "late failure") {
		t.Fatalf("unexpected exhaustion error: %v", err)
	}
}

// The error-body read must be capped so a runaway body cannot grow the error unbounded,
// and an empty body must still be reported.
func TestErrorBodyReadIsCappedAndEmptyBodyReported(t *testing.T) {
	capped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("x", 20000)))
	}))
	defer capped.Close()

	client := NewResilientClient(RetryConfig{Client: capped.Client()})
	_, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", capped.URL, nil)
	})
	if err == nil {
		t.Fatal("expected error for capped body")
	}
	if len(err.Error()) > 4200 {
		t.Fatalf("error body not capped: %d bytes", len(err.Error()))
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer empty.Close()

	client2 := NewResilientClient(RetryConfig{MaxRetries: 0, Client: empty.Client()})
	_, err = client2.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", empty.URL, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "(empty)") {
		t.Fatalf("expected empty-body marker, got %v", err)
	}
}

// OnAttempt is the observation hook; it must fire for successful attempts too.
func TestOnAttemptObservesSuccessfulAttempt(t *testing.T) {
	var seen []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewResilientClient(RetryConfig{
		MaxRetries: 1,
		Client:     server.Client(),
		OnAttempt: func(attempt int) {
			seen = append(seen, attempt)
		},
	})

	resp, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if len(seen) != 1 || seen[0] != 0 {
		t.Fatalf("expected OnAttempt(0), got %v", seen)
	}
}
