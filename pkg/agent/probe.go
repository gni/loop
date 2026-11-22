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
	"loop/pkg/config"
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
	supported, detected, probedCaps := p.probeServerCapabilitiesHTTP(ctx)
	p.thinkingMu.Lock()
	p.ThinkingSupported = supported
	p.ThinkingSupportChecked = true
	p.ProbedCapabilities = probedCaps
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
func (p *OpenAICompatibleProvider) probeServerCapabilitiesHTTP(ctx context.Context) (bool, int, config.ChatCapabilities) {
	timeout := 5 * time.Second
	if p.Config != nil && p.Config.Timeout > 0 && time.Duration(p.Config.Timeout)*time.Second < timeout {
		timeout = time.Duration(p.Config.Timeout) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if p.Config == nil || p.Config.Endpoint == "" {
		return false, 0, config.ChatCapabilities{}
	}
	base, err := url.Parse(p.Config.Endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return false, 0, config.ChatCapabilities{}
	}
	// llama.cpp exposes /props at the server root, even when chat uses /v1.
	base.Path = "/props"
	base.RawPath = ""
	query := url.Values{}
	query.Set("model", p.Config.ResolveModelAlias(p.Config.Model))
	base.RawQuery = query.Encode()
	base.Fragment = ""
	supported, detected := false, 0
	var probedCaps config.ChatCapabilities
	if p.api() == "chat-completions" || p.api() == "responses" {
		var props struct {
			NCtx              int             `json:"n_ctx"`
			MaxContext        int             `json:"max_context"`
			EngineMaxContext  int             `json:"engine_max_context"`
			SupportsReasoning json.RawMessage `json:"supports_reasoning"`
			SupportsThinking  json.RawMessage `json:"supports_thinking"`
			Thinking          json.RawMessage `json:"thinking"`
			ChatTemplate      string          `json:"chat_template"`
			ChatTemplateCaps  struct {
				SupportsParallelToolCalls bool `json:"supports_parallel_tool_calls"`
				SupportsPreserveReasoning bool `json:"supports_preserve_reasoning"`
				SupportsReasoning         bool `json:"supports_reasoning"`
				SupportsThinking          bool `json:"supports_thinking"`
			} `json:"chat_template_caps"`
			DefaultGenerationSettings struct {
				NCtx       int                    `json:"n_ctx"`
				MaxContext int                    `json:"max_context"`
				Params     map[string]interface{} `json:"params"`
			} `json:"default_generation_settings"`
		}
		readProps := p.readServerMetadata(checkCtx, base.String(), 128*1024, &props)
		if !readProps && base.RawQuery != "" {
			// Single-model servers (such as Strata or llama.cpp) return 404 on
			// /props?model=<alias> when the client uses a local alias (e.g. "op"),
			// while GET /props without a query string returns the active model's metadata.
			base.RawQuery = ""
			readProps = p.readServerMetadata(checkCtx, base.String(), 128*1024, &props)
		}
		if readProps {
			for _, flag := range []json.RawMessage{props.SupportsReasoning, props.SupportsThinking, props.Thinking} {
				var enabled bool
				if json.Unmarshal(flag, &enabled) == nil && enabled {
					supported = true
				}
			}
			if props.ChatTemplateCaps.SupportsPreserveReasoning || props.ChatTemplateCaps.SupportsReasoning || props.ChatTemplateCaps.SupportsThinking {
				supported = true
			}
			if strings.Contains(props.ChatTemplate, "enable_thinking") || strings.Contains(props.ChatTemplate, "reasoning_effort") || strings.Contains(props.ChatTemplate, "<think>") {
				supported = true
			}
			if props.ChatTemplate != "" || len(props.DefaultGenerationSettings.Params) > 0 {
				probedCaps.Temperature = true
				probedCaps.StreamOptions = true
				probedCaps.Penalties = true
			}
			if props.ChatTemplateCaps.SupportsParallelToolCalls {
				probedCaps.ParallelToolCalls = true
			}
			if supported {
				probedCaps.ThinkingControls = true
				probedCaps.ReasoningEffort = true
			}
			detected = smallestContextLimit(props.NCtx, props.MaxContext, props.EngineMaxContext, props.DefaultGenerationSettings.NCtx, props.DefaultGenerationSettings.MaxContext)
		}
	}
	if detected == 0 {
		endpoint, err := transporthttp.ModelDiscoveryURL(p.Config.Endpoint, p.api())
		var catalog modelCatalog
		if err == nil && p.readServerMetadata(checkCtx, endpoint, 4<<20, &catalog) {
			selected := strings.TrimPrefix(p.Config.ResolveModelAlias(p.Config.Model), "models/")
			entries := append(catalog.Data, catalog.Models...)
			for _, entry := range entries {
				if entry.modelID() == selected {
					detected = smallestContextLimit(detected, entry.contextLimit())
				}
			}
			if detected == 0 && len(entries) == 1 {
				detected = smallestContextLimit(detected, entries[0].contextLimit())
			}
		}
	}
	return supported, detected, probedCaps
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
	supported, detected, probedCaps := p.probeServerCapabilitiesHTTP(ctx)
	p.thinkingMu.Lock()
	p.ThinkingSupported = supported
	p.ThinkingSupportChecked = true
	p.ProbedCapabilities = probedCaps
	p.thinkingMu.Unlock()
	p.ContextLimitMu.Lock()
	if detected > 0 {
		p.DetectedContextLimit = detected
	}
	p.ContextLimitChecked = true
	p.ContextLimitMu.Unlock()
}
