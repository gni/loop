package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	transporthttp "loop/pkg/transport/http"
)

type cachedTokenCount struct {
	count   int
	expires time.Time
}

// Only hashes and counts are cached; prompts and credentials are never retained.
type requestTokenCounter struct {
	mu         sync.Mutex
	cache      map[[32]byte]cachedTokenCount
	order      [][32]byte
	retryAfter time.Time
}

func (p *OpenAICompatibleProvider) requestCounter() *requestTokenCounter {
	p.tokenCounterMu.Lock()
	defer p.tokenCounterMu.Unlock()
	if p.tokenCounter == nil {
		p.tokenCounter = &requestTokenCounter{cache: make(map[[32]byte]cachedTokenCount)}
	}
	return p.tokenCounter
}

// countRequestTokens uses llama.cpp's optional chat input_tokens route on the
// configured endpoint. Unsupported or unavailable servers use the estimate.
// This runs under the existing inference lease and never generates model text.
func (p *OpenAICompatibleProvider) countRequestTokens(ctx context.Context, messages []db.Message, tools []tool.Tool, output int) (int, bool) {
	if ctx == nil || ctx.Err() != nil || p.Config == nil || p.api() != "chat-completions" {
		return 0, false
	}
	state := p.requestCounter()
	state.mu.Lock()
	coolingDown := time.Now().Before(state.retryAfter)
	state.mu.Unlock()
	if coolingDown {
		return 0, false
	}
	reqBody, err := p.chatCompletionRequest(messages, tools, output)
	if err != nil {
		return 0, false
	}
	reqBody.Stream, reqBody.StreamOptions = false, nil
	data, err := marshalChatCompletionRequest(reqBody)
	if err != nil || len(data) > 4<<20 {
		return 0, false
	}
	key := sha256.Sum256(data)
	state.mu.Lock()
	cached, exists := state.cache[key]
	state.mu.Unlock()
	if exists && time.Now().Before(cached.expires) {
		return cached.count, true
	}
	endpoint, err := url.Parse(completionURL(p.Config.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return 0, false
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/input_tokens"
	endpoint.RawPath, endpoint.Fragment = "", ""
	countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(countCtx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
		transporthttp.ApplyProviderHeaders(req.Header, profile.Headers)
	}
	transporthttp.ApplyProviderAuthHeaders(req.Header, "chat-completions", p.requestKey(ctx))
	client := http.DefaultClient
	if p.HTTPClient != nil {
		client = p.HTTPClient
	}
	// Keep optional accounting on the same endpoint, including redirects.
	localClient := *client
	localClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := localClient.Do(req)
	retryDelay := time.Minute
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8193))
			var result struct {
				InputTokens *int `json:"input_tokens"`
			}
			if readErr == nil && len(body) <= 8192 && json.Unmarshal(body, &result) == nil && result.InputTokens != nil && *result.InputTokens > 0 && *result.InputTokens <= 1<<26 {
				state.mu.Lock()
				if _, exists := state.cache[key]; !exists {
					if len(state.order) >= 32 {
						delete(state.cache, state.order[0])
						state.order = state.order[1:]
					}
					state.order = append(state.order, key)
				}
				state.cache[key] = cachedTokenCount{*result.InputTokens, time.Now().Add(time.Minute)}
				state.mu.Unlock()
				return *result.InputTokens, true
			}
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
			retryDelay = 10 * time.Minute
		}
	}
	if ctx.Err() == nil {
		state.mu.Lock()
		state.retryAfter = time.Now().Add(retryDelay)
		state.mu.Unlock()
	}
	return 0, false
}
