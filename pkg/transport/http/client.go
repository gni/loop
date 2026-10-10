package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// RetryConfig encapsulates resilience and backoff parameters.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	Client     *http.Client
	OnAttempt  func(attempt int)
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
	return code >= 500
}

// DoWithRetry executes an HTTP request, retrying on transient errors and 5xx responses.
func (c *ResilientClient) DoWithRetry(ctx context.Context, createReq func() (*http.Request, error)) (*http.Response, error) {
	var resp *http.Response
	var lastErr error

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * c.config.BaseDelay
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

		req, err := createReq()
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err = c.config.Client.Do(req)
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
			if bodyErr != nil {
				lastErr = fmt.Errorf("server returned non-200 status: %d. Body read failed: %v", resp.StatusCode, bodyErr)
			} else if len(body) == 0 {
				lastErr = fmt.Errorf("server returned non-200 status: %d. Body: (empty)", resp.StatusCode)
			} else {
				lastErr = fmt.Errorf("server returned non-200 status: %d. Body: %s", resp.StatusCode, string(body))
			}

			if IsRetryableStatus(resp.StatusCode) {
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

// NewHTTPClient returns a configured *http.Client with resilient connection pooling and TLS options.
func NewHTTPClient(tlsConfig *tls.Config, timeoutSeconds int) *http.Client {
	dialerTimeout := 30 * time.Second
	if timeoutSeconds > 0 && time.Duration(timeoutSeconds)*time.Second < dialerTimeout {
		dialerTimeout = time.Duration(timeoutSeconds) * time.Second
	}
	dialer := &net.Dialer{
		Timeout:   dialerTimeout,
		KeepAlive: 30 * time.Second,
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}

	return &http.Client{
		Transport: transport,
		Timeout:   0, // Streaming requests are bounded by request context timeout
	}
}
