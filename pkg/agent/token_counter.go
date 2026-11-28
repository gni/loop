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

type tokenCounterMode int

const (
	tokenCounterAuto tokenCounterMode = iota
	tokenCounterInputTokens
	tokenCounterLlamaTokenize
)

// Only hashes and counts are cached; prompts and credentials are never retained.
type requestTokenCounter struct {
	mu         sync.Mutex
	cache      map[[32]byte]cachedTokenCount
	order      [][32]byte
	retryAfter time.Time
	scale      float64
	mode       tokenCounterMode
}

func (c *requestTokenCounter) estimateScale() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return max(1, c.scale)
}

func (c *requestTokenCounter) observe(measured, estimated int) {
	if measured <= 0 || estimated <= 0 {
		return
	}
	c.mu.Lock()
	c.scale = max(c.scale, float64(measured)/float64(estimated))
	c.mu.Unlock()
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
// configured endpoint, falling back to /apply-template + /tokenize on standard
// llama-server instances. Unsupported or unavailable servers use the estimate.
// This runs under the existing inference lease and never generates model text.
func (p *OpenAICompatibleProvider) countRequestTokens(ctx context.Context, messages []db.Message, tools []tool.Tool, output int) (int, bool) {
	if ctx == nil || ctx.Err() != nil || p.Config == nil || p.api() != "chat-completions" {
		return 0, false
	}
	state := p.requestCounter()
	state.mu.Lock()
	now := time.Now()
	coolingDown := now.Before(state.retryAfter)
	mode := state.mode
	usableCache := false
	if coolingDown {
		for _, cached := range state.cache {
			if now.Before(cached.expires) {
				usableCache = true
				break
			}
		}
	}
	state.mu.Unlock()
	// An unavailable counter with no live cached counts needs no request
	// encoding. Warm counts still take the normal hash lookup below.
	if coolingDown && !usableCache {
		return 0, false
	}
	preserveReasoning := p.PreserveReasoningEnabled()
	reqBody, err := p.chatCompletionRequest(messages, tools, output)
	if err != nil {
		return 0, false
	}
	reqBody.Stream, reqBody.StreamOptions = false, nil
	data, err := marshalChatCompletionRequestWithOptions(reqBody, preserveReasoning)
	if err != nil || len(data) > 4<<20 {
		return 0, false
	}
	keyData := data
	if reqBody.MaxTokens != 0 || reqBody.MaxCompletionTokens != 0 {
		keyRequest := reqBody
		keyRequest.MaxTokens, keyRequest.MaxCompletionTokens = 0, 0
		keyData, err = marshalChatCompletionRequestWithOptions(keyRequest, preserveReasoning)
		if err != nil {
			return 0, false
		}
	}
	key := sha256.Sum256(keyData)
	state.mu.Lock()
	cached, exists := state.cache[key]
	coolingDown = time.Now().Before(state.retryAfter)
	mode = state.mode
	state.mu.Unlock()
	if exists && time.Now().Before(cached.expires) {
		return cached.count, true
	}
	if coolingDown {
		return 0, false
	}
	endpoint, err := url.Parse(completionURL(p.Config.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return 0, false
	}
	countCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	client := http.DefaultClient
	if p.HTTPClient != nil {
		client = p.HTTPClient
	}
	// Keep optional accounting on the same endpoint, including redirects.
	localClient := *client
	localClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	recordCount := func(count int, detectedMode tokenCounterMode) (int, bool) {
		state.mu.Lock()
		if _, exists := state.cache[key]; !exists {
			if len(state.order) >= 32 {
				delete(state.cache, state.order[0])
				state.order = state.order[1:]
			}
			state.order = append(state.order, key)
		}
		state.cache[key] = cachedTokenCount{count, time.Now().Add(time.Minute)}
		state.mode = detectedMode
		state.mu.Unlock()
		state.observe(count, requestTokensFor(reqBody.Messages, preserveReasoning)+EstimateToolSchemaTokens(tools))
		return count, true
	}

	retryDelay := time.Minute
	if mode == tokenCounterAuto || mode == tokenCounterInputTokens {
		count, status, ok := p.countViaInputTokens(countCtx, &localClient, endpoint, data)
		if ok {
			return recordCount(count, tokenCounterInputTokens)
		}
		notSupported := status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented
		if notSupported {
			retryDelay = 10 * time.Minute
			if mode == tokenCounterAuto {
				llamaCount, llamaStatus, llamaOK := p.countViaLlamaTokenize(countCtx, &localClient, endpoint, reqBody, preserveReasoning)
				if llamaOK {
					return recordCount(llamaCount, tokenCounterLlamaTokenize)
				}
				if llamaStatus != http.StatusNotFound && llamaStatus != http.StatusMethodNotAllowed && llamaStatus != http.StatusNotImplemented && llamaStatus != 0 {
					retryDelay = time.Minute
				}
			}
		}
	} else if mode == tokenCounterLlamaTokenize {
		llamaCount, llamaStatus, llamaOK := p.countViaLlamaTokenize(countCtx, &localClient, endpoint, reqBody, preserveReasoning)
		if llamaOK {
			return recordCount(llamaCount, tokenCounterLlamaTokenize)
		}
		if llamaStatus == http.StatusNotFound || llamaStatus == http.StatusMethodNotAllowed || llamaStatus == http.StatusNotImplemented {
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

func (p *OpenAICompatibleProvider) newTokenCountRequest(ctx context.Context, rawURL string, payload []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
		transporthttp.ApplyProviderHeaders(req.Header, profile.Headers)
	}
	transporthttp.ApplyProviderAuthHeaders(req.Header, "chat-completions", p.requestKey(ctx))
	return req, nil
}

func (p *OpenAICompatibleProvider) countViaInputTokens(ctx context.Context, client *http.Client, chatEndpoint *url.URL, data []byte) (int, int, bool) {
	ep := *chatEndpoint
	ep.Path = strings.TrimRight(ep.Path, "/") + "/input_tokens"
	ep.RawPath, ep.Fragment = "", ""
	req, err := p.newTokenCountRequest(ctx, ep.String(), data)
	if err != nil {
		return 0, 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode, false
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8193))
	var result struct {
		InputTokens *int `json:"input_tokens"`
	}
	if readErr == nil && len(body) <= 8192 && json.Unmarshal(body, &result) == nil && result.InputTokens != nil && *result.InputTokens > 0 && *result.InputTokens <= 1<<26 {
		return *result.InputTokens, resp.StatusCode, true
	}
	return 0, resp.StatusCode, false
}

// countViaLlamaTokenize uses llama-server's standard /apply-template and
// /tokenize endpoints to measure exact prompt tokens when /input_tokens is absent.
func (p *OpenAICompatibleProvider) countViaLlamaTokenize(ctx context.Context, client *http.Client, chatEndpoint *url.URL, reqBody ChatCompletionRequest, preserveReasoning bool) (int, int, bool) {
	wireMessages := buildWireChatMessages(reqBody.Messages, preserveReasoning)
	templatePayload, err := json.Marshal(struct {
		Messages []wireChatMessage `json:"messages"`
	}{Messages: wireMessages})
	if err != nil || len(templatePayload) > 4<<20 {
		return 0, 0, false
	}

	applyURL := *chatEndpoint
	applyURL.Path = "/apply-template"
	applyURL.RawPath, applyURL.RawQuery, applyURL.Fragment = "", "", ""
	applyReq, err := p.newTokenCountRequest(ctx, applyURL.String(), templatePayload)
	if err != nil {
		return 0, 0, false
	}
	applyResp, err := client.Do(applyReq)
	if err != nil {
		return 0, 0, false
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		return 0, applyResp.StatusCode, false
	}
	applyBody, err := io.ReadAll(io.LimitReader(applyResp.Body, 4<<20+1))
	if err != nil || len(applyBody) > 4<<20 {
		return 0, applyResp.StatusCode, false
	}
	var applied struct {
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(applyBody, &applied) != nil || applied.Prompt == "" {
		return 0, applyResp.StatusCode, false
	}

	tokenizePayload, err := json.Marshal(struct {
		Content      string `json:"content"`
		AddSpecial   bool   `json:"add_special"`
		ParseSpecial bool   `json:"parse_special"`
		WithPieces   bool   `json:"with_pieces"`
	}{
		Content:      applied.Prompt,
		AddSpecial:   false,
		ParseSpecial: true,
		WithPieces:   false,
	})
	if err != nil || len(tokenizePayload) > 4<<20 {
		return 0, 0, false
	}

	tokURL := *chatEndpoint
	tokURL.Path = "/tokenize"
	tokURL.RawPath, tokURL.RawQuery, tokURL.Fragment = "", "", ""
	tokReq, err := p.newTokenCountRequest(ctx, tokURL.String(), tokenizePayload)
	if err != nil {
		return 0, 0, false
	}
	tokResp, err := client.Do(tokReq)
	if err != nil {
		return 0, 0, false
	}
	defer tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusOK {
		return 0, tokResp.StatusCode, false
	}
	tokBody, err := io.ReadAll(io.LimitReader(tokResp.Body, 8<<20+1))
	if err != nil || len(tokBody) > 8<<20 {
		return 0, tokResp.StatusCode, false
	}
	var tokenized struct {
		Tokens []json.RawMessage `json:"tokens"`
		Count  int               `json:"count"`
	}
	if json.Unmarshal(tokBody, &tokenized) != nil {
		return 0, tokResp.StatusCode, false
	}
	count := tokenized.Count
	if count <= 0 {
		count = len(tokenized.Tokens)
	}
	if count <= 0 || count > 1<<26 {
		return 0, tokResp.StatusCode, false
	}
	// /apply-template only formats messages; add schema estimate when native tools are sent.
	total := count + EstimateToolSchemaTokens(reqBody.Tools)
	return total, tokResp.StatusCode, true
}
