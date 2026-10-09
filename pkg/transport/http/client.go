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
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "certificate") ||
		strings.Contains(errStr, "tls:") ||
		strings.Contains(errStr, "x509:") ||
		strings.Contains(errStr, "unsupported protocol scheme") ||
		strings.Contains(errStr, "cannot assign requested address") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "deadline exceeded")
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
			if IsNonRetryableError(err) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server returned non-200 status: %d. Body: %s", resp.StatusCode, string(body))

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
	dialerTimeout := 10 * time.Second
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
