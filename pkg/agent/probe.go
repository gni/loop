package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"loop/pkg/buildinfo"
	transporthttp "loop/pkg/transport/http"
)

func (p *OpenAICompatibleProvider) GetDetectedContextLimit() int {
	p.ContextLimitMu.RLock()
	defer p.ContextLimitMu.RUnlock()
	return p.DetectedContextLimit
}

func (p *OpenAICompatibleProvider) CheckThinkingSupport(ctx context.Context) bool {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	supported, detected := p.probeServerCapabilitiesHTTP(ctx)
	p.thinkingMu.Lock()
	p.ThinkingSupported = supported
	p.ThinkingSupportChecked = true
	p.thinkingMu.Unlock()
	p.ContextLimitMu.Lock()
	if detected > 0 {
		p.DetectedContextLimit = detected
	}
	p.ContextLimitChecked = true
	p.ContextLimitMu.Unlock()
	return supported
}

// Probe metadata without holding status locks. Both routes share one deadline.
func (p *OpenAICompatibleProvider) probeServerCapabilitiesHTTP(ctx context.Context) (bool, int) {
	timeout := 5 * time.Second
	if p.Config != nil && p.Config.Timeout > 0 && time.Duration(p.Config.Timeout)*time.Second < timeout {
		timeout = time.Duration(p.Config.Timeout) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if p.Config == nil || p.Config.Endpoint == "" {
		return false, 0
	}
	base, err := url.Parse(p.Config.Endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return false, 0
	}
	// llama.cpp exposes /props at the server root, even when chat uses /v1.
	base.Path = "/props"
	base.RawPath = ""
	query := url.Values{}
	query.Set("model", p.Config.ResolveModelAlias(p.Config.Model))
	base.RawQuery = query.Encode()
	base.Fragment = ""
	supported, detected := false, 0
	if p.api() == "chat-completions" || p.api() == "responses" {
		var props struct {
			NCtx                      int             `json:"n_ctx"`
			MaxContext                int             `json:"max_context"`
			EngineMaxContext          int             `json:"engine_max_context"`
			SupportsReasoning         json.RawMessage `json:"supports_reasoning"`
			SupportsThinking          json.RawMessage `json:"supports_thinking"`
			Thinking                  json.RawMessage `json:"thinking"`
			DefaultGenerationSettings struct {
				NCtx       int `json:"n_ctx"`
				MaxContext int `json:"max_context"`
			} `json:"default_generation_settings"`
		}
		if p.readServerMetadata(checkCtx, base.String(), 128*1024, &props) {
			for _, flag := range []json.RawMessage{props.SupportsReasoning, props.SupportsThinking, props.Thinking} {
				var enabled bool
				if json.Unmarshal(flag, &enabled) == nil && enabled {
					supported = true
				}
			}
			detected = smallestContextLimit(props.NCtx, props.MaxContext, props.EngineMaxContext, props.DefaultGenerationSettings.NCtx, props.DefaultGenerationSettings.MaxContext)
		}
	}
	if detected == 0 {
		endpoint, err := transporthttp.ModelDiscoveryURL(p.Config.Endpoint, p.api())
		var catalog modelCatalog
		if err == nil && p.readServerMetadata(checkCtx, endpoint, 4<<20, &catalog) {
			selected := strings.TrimPrefix(p.Config.ResolveModelAlias(p.Config.Model), "models/")
			for _, entry := range append(catalog.Data, catalog.Models...) {
				if entry.modelID() == selected {
					detected = smallestContextLimit(detected, entry.contextLimit())
				}
			}
		}
	}
	return supported, detected
}

func (p *OpenAICompatibleProvider) readServerMetadata(ctx context.Context, endpoint string, limit int64, target any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	req.Header.Set("loop", buildinfo.Version)
	api := p.api()
	if api != "chat-completions" {
		transporthttp.ApplyProviderAuthHeaders(req.Header, api, p.requestKey(ctx))
	}
	if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
		transporthttp.ApplyProviderHeaders(req.Header, profile.Headers)
	}
	if api == "chat-completions" {
		transporthttp.ApplyProviderAuthHeaders(req.Header, api, p.requestKey(ctx))
	}
	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	return err == nil && int64(len(body)) <= limit && json.Unmarshal(body, target) == nil
}

// ProbeServerCapabilities reads explicit reasoning metadata, if present, and
// discovers the active context window. HTTP 200 alone proves neither capability.
func (p *OpenAICompatibleProvider) ProbeServerCapabilities(ctx context.Context) {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	if p.ThinkingProbeChecked() {
		return
	}
	supported, detected := p.probeServerCapabilitiesHTTP(ctx)
	p.thinkingMu.Lock()
	p.ThinkingSupported = supported
	p.ThinkingSupportChecked = true
	p.thinkingMu.Unlock()
	p.ContextLimitMu.Lock()
	if detected > 0 {
		p.DetectedContextLimit = detected
	}
	p.ContextLimitChecked = true
	p.ContextLimitMu.Unlock()
}
