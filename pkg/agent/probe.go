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

type probedServerInfo struct {
	thinkingSupported bool
	detectedContext   int
	probedCaps        config.ChatCapabilities
	preserveReasoning bool
	visionKnown       bool
	visionSupported   bool
	templateEfforts   map[string]bool
	complete          bool
}

func parseTemplateReasoningEfforts(chatTemplate string) map[string]bool {
	if chatTemplate == "" || !strings.Contains(chatTemplate, "reasoning_effort") {
		return nil
	}
	// Detect effort level keys declared inside Jinja effort maps or comparisons
	// (e.g. Strata's effort_map = {"none": 0.0, "minimal": 0.1, ..., "max": 0.99}).
	candidates := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	found := make(map[string]bool)
	for _, key := range candidates {
		if strings.Contains(chatTemplate, `"`+key+`"`) || strings.Contains(chatTemplate, `'`+key+`'`) {
			found[key] = true
		}
	}
	if len(found) == 0 {
		return nil
	}
	return found
}

func (p *OpenAICompatibleProvider) applyProbedInfo(info probedServerInfo) {
	if !info.complete {
		return
	}
	p.thinkingMu.Lock()
	p.ThinkingSupported = info.thinkingSupported
	p.ThinkingSupportChecked = true
	p.ProbedCapabilities = info.probedCaps
	p.PreserveReasoningSupported = info.preserveReasoning
	p.ProbedVisionKnown = info.visionKnown
	p.ProbedVisionSupported = info.visionSupported
	p.ProbedEfforts = info.templateEfforts
	p.thinkingMu.Unlock()
	p.ContextLimitMu.Lock()
	if info.detectedContext > 0 {
		p.DetectedContextLimit = info.detectedContext
	}
	p.ContextLimitChecked = true
	p.ContextLimitMu.Unlock()
}

func (p *OpenAICompatibleProvider) CheckThinkingSupport(ctx context.Context) bool {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	info := p.probeServerCapabilitiesHTTP(ctx)
	p.applyProbedInfo(info)
	return info.thinkingSupported
}

// Probe metadata without holding status locks. Both routes share one deadline.
func (p *OpenAICompatibleProvider) probeServerCapabilitiesHTTP(ctx context.Context) probedServerInfo {
	timeout := 5 * time.Second
	if p.Config != nil && p.Config.Timeout > 0 && time.Duration(p.Config.Timeout)*time.Second < timeout {
		timeout = time.Duration(p.Config.Timeout) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if p.Config == nil || p.Config.Endpoint == "" {
		return probedServerInfo{complete: true}
	}
	if checkCtx.Value(credentialContextKey{}) == nil && p.ResolveCredential != nil {
		if key, err := p.ResolveCredential(checkCtx); err == nil && key != "" {
			checkCtx = context.WithValue(checkCtx, credentialContextKey{}, key)
		}
	}
	base, err := url.Parse(p.Config.Endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return probedServerInfo{complete: true}
	}
	// llama.cpp exposes /props at the server root, even when chat uses /v1.
	base.Path = "/props"
	base.RawPath = ""
	query := url.Values{}
	query.Set("model", p.Config.ResolveModelAlias(p.Config.Model))
	base.RawQuery = query.Encode()
	base.Fragment = ""
	var info probedServerInfo
	authRejected := false
	if p.api() == "chat-completions" || p.api() == "responses" {
		var props struct {
			NCtx              int             `json:"n_ctx"`
			MaxContext        int             `json:"max_context"`
			EngineMaxContext  int             `json:"engine_max_context"`
			SupportsReasoning json.RawMessage `json:"supports_reasoning"`
			SupportsThinking  json.RawMessage `json:"supports_thinking"`
			Thinking          json.RawMessage `json:"thinking"`
			ChatTemplate      string          `json:"chat_template"`
			Modalities        struct {
				Vision *bool `json:"vision"`
			} `json:"modalities"`
			ChatTemplateCaps struct {
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
		readProps, status := p.readServerMetadataStatus(checkCtx, base.String(), 128*1024, &props)
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			authRejected = true
		}
		if !readProps && base.RawQuery != "" && !authRejected {
			// Single-model servers (such as Strata or llama.cpp) return 404 on
			// /props?model=<alias> when the client uses a local alias (e.g. "op"),
			// while GET /props without a query string returns the active model's metadata.
			base.RawQuery = ""
			readProps, status = p.readServerMetadataStatus(checkCtx, base.String(), 128*1024, &props)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				authRejected = true
			}
		}
		if readProps {
			for _, flag := range []json.RawMessage{props.SupportsReasoning, props.SupportsThinking, props.Thinking} {
				var enabled bool
				if json.Unmarshal(flag, &enabled) == nil && enabled {
					info.thinkingSupported = true
				}
			}
			if props.ChatTemplateCaps.SupportsPreserveReasoning {
				info.preserveReasoning = true
				info.thinkingSupported = true
			}
			if props.ChatTemplateCaps.SupportsReasoning || props.ChatTemplateCaps.SupportsThinking {
				info.thinkingSupported = true
			}
			if strings.Contains(props.ChatTemplate, "enable_thinking") || strings.Contains(props.ChatTemplate, "reasoning_effort") || strings.Contains(props.ChatTemplate, "<think>") {
				info.thinkingSupported = true
			}
			info.templateEfforts = parseTemplateReasoningEfforts(props.ChatTemplate)
			if props.Modalities.Vision != nil {
				info.visionKnown = true
				info.visionSupported = *props.Modalities.Vision
			}
			if props.ChatTemplate != "" || len(props.DefaultGenerationSettings.Params) > 0 {
				info.probedCaps.Temperature = true
				info.probedCaps.StreamOptions = true
				info.probedCaps.Penalties = true
			}
			if props.ChatTemplateCaps.SupportsParallelToolCalls {
				info.probedCaps.ParallelToolCalls = true
			}
			if info.thinkingSupported {
				info.probedCaps.ThinkingControls = true
				info.probedCaps.ReasoningEffort = true
			}
			info.detectedContext = smallestContextLimit(props.NCtx, props.MaxContext, props.EngineMaxContext, props.DefaultGenerationSettings.NCtx, props.DefaultGenerationSettings.MaxContext)
		}
	}
	if info.detectedContext == 0 || !info.visionKnown {
		endpoint, err := transporthttp.ModelDiscoveryURL(p.Config.Endpoint, p.api())
		var catalog modelCatalog
		if err == nil {
			ok, status := p.readServerMetadataStatus(checkCtx, endpoint, 4<<20, &catalog)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				authRejected = true
			}
			if ok {
				selected := strings.TrimPrefix(p.Config.ResolveModelAlias(p.Config.Model), "models/")
				entries := append(catalog.Data, catalog.Models...)
				applyCatalogEntry := func(entry modelMetadata) {
					if info.detectedContext == 0 {
						info.detectedContext = smallestContextLimit(info.detectedContext, entry.contextLimit())
					}
					if !info.visionKnown {
						if vision, known := entry.visionSupport(); known {
							info.visionKnown = true
							info.visionSupported = vision
						}
					}
				}
				matched := false
				for _, entry := range entries {
					if entry.modelID() == selected {
						matched = true
						applyCatalogEntry(entry)
					}
				}
				if !matched && len(entries) == 1 {
					applyCatalogEntry(entries[0])
				}
			}
		}
	}
	info.complete = ctx.Err() == nil && (!authRejected || info.thinkingSupported || info.detectedContext > 0 || info.visionKnown)
	return info
}

func (p *OpenAICompatibleProvider) readServerMetadata(ctx context.Context, endpoint string, limit int64, target any) bool {
	ok, _ := p.readServerMetadataStatus(ctx, endpoint, limit, target)
	return ok
}

func (p *OpenAICompatibleProvider) readServerMetadataStatus(ctx context.Context, endpoint string, limit int64, target any) (bool, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, 0
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
		return false, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, resp.StatusCode
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	return err == nil && int64(len(body)) <= limit && json.Unmarshal(body, target) == nil, resp.StatusCode
}

// ProbeServerCapabilities reads explicit reasoning metadata, if present, and
// discovers the active context window. HTTP 200 alone proves neither capability.
func (p *OpenAICompatibleProvider) ProbeServerCapabilities(ctx context.Context) {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	if p.ThinkingProbeChecked() {
		return
	}
	info := p.probeServerCapabilitiesHTTP(ctx)
	p.applyProbedInfo(info)
}
