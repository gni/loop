package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"loop/pkg/transport/retry"
)

// RetryConfig encapsulates resilience and backoff parameters.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	Client     *http.Client
	OnAttempt  func(attempt int)
	Backoff    func(ceiling time.Duration) time.Duration
	// BeforeAttempt runs after the backoff sleep, before the request is issued. It may
	// return an error to abort the retry loop (used by the streaming provider to report
	// a watchdog timeout rather than a plain context cancellation).
	BeforeAttempt func(attempt int) error
	// DescribeError, when set, wraps a transport error so callers can add context such
	// as the endpoint that failed.
	DescribeError func(err error) error
}

// ResilientClient executes HTTP requests with exponential backoff and error classification.
type ResilientClient struct {
	config RetryConfig
}

// NewResilientClient instantiates a resilient HTTP wrapper.
func NewResilientClient(cfg RetryConfig) *ResilientClient {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 1 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	return &ResilientClient{config: cfg}
}

// IsNonRetryableError returns true when an error indicates a permanent network or TLS failure.
func IsNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "certificate") ||
		strings.Contains(errStr, "tls:") ||
		strings.Contains(errStr, "x509:") ||
		strings.Contains(errStr, "unsupported protocol scheme") ||
		strings.Contains(errStr, "cannot assign requested address") ||
		strings.Contains(errStr, "no route to host")
}

// IsRetryableStatus returns true if an HTTP status code warrants a retry (e.g., 5xx server errors).
func IsRetryableStatus(code int) bool {
	return code == 408 || code == 429 || (code >= 500 && code <= 599)
}

// DoWithRetry executes an HTTP request, retrying on transient errors and 5xx responses.
func (c *ResilientClient) DoWithRetry(ctx context.Context, createReq func() (*http.Request, error)) (*http.Response, error) {
	var resp *http.Response
	var lastErr error
	var retryDelay time.Duration

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			multiplier := time.Duration(1 << min(attempt-1, 10))
			ceiling := min(c.config.BaseDelay, maxRetryDelay/multiplier) * multiplier
			backoff := time.Duration(rand.Int64N(max(1, int64(ceiling))))
			if c.config.Backoff != nil {
				backoff = min(ceiling, max(0, c.config.Backoff(ceiling)))
			}
			if retryDelay > backoff {
				backoff = retryDelay
			}
			retryDelay = 0
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			if c.config.BeforeAttempt != nil {
				if err := c.config.BeforeAttempt(attempt); err != nil {
					return nil, err
				}
			}
			if c.config.OnAttempt != nil {
				c.config.OnAttempt(attempt)
			}
		}

		if err := retry.Claim(ctx); err != nil {
			return nil, errors.Join(lastErr, err)
		}
		req, err := createReq()
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err = c.config.Client.Do(req.WithContext(ctx))
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			if c.config.DescribeError != nil {
				lastErr = c.config.DescribeError(err)
			}
			if IsNonRetryableError(err) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			// Cap the read so a hostile or runaway error body cannot grow unbounded, and
			// surface a read failure instead of embedding a possibly empty body.
			const maxErrorBodyBytes = 4096
			body, bodyErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
			resp.Body.Close()
			lastErr = &StatusError{Code: resp.StatusCode, Body: string(body), ReadError: bodyErr}

			if IsRetryableStatus(resp.StatusCode) {
				retryDelay = serverRetryDelay(resp.Header, time.Now())
				continue
			}
			return nil, lastErr
		}

		lastErr = nil
		if c.config.OnAttempt != nil {
			c.config.OnAttempt(attempt)
		}
		return resp, nil
	}

	if lastErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("after %d retries: %w", c.config.MaxRetries, lastErr)
	}

	return resp, nil
}

const maxRetryDelay = 30 * time.Second

// serverRetryDelay accepts provider millisecond hints and standard Retry-After
// seconds or dates. Clamp integers before converting them to a duration.
func serverRetryDelay(header http.Header, now time.Time) time.Duration {
	if delay, ok := parseRetryDelay(header.Get("retry-after-ms"), time.Millisecond); ok {
		return delay
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if delay, ok := parseRetryDelay(value, time.Second); ok {
		return delay
	}
	if deadline, err := http.ParseTime(value); err == nil {
		return min(max(0, deadline.Sub(now)), maxRetryDelay)
	}
	return 0
}

func parseRetryDelay(value string, unit time.Duration) (time.Duration, bool) {
	count, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(min(count, uint64(maxRetryDelay/unit))) * unit, true
}
