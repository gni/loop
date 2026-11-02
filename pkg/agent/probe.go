package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
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
	if detected > 0 {
		p.ContextLimitMu.Lock()
		p.DetectedContextLimit = detected
		p.ContextLimitChecked = true
		p.ContextLimitMu.Unlock()
	}
	return supported
}

// probeServerCapabilitiesHTTP performs the /props HTTP probe without holding
// thinkingMu or ContextLimitMu so status readers never stall on network I/O.
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
	if err != nil || base.Scheme == "" || base.Host == "" {
		return false, 0
	}
	// llama.cpp exposes /props at the server root, even when chat uses /v1.
	base.Path = "/props"
	base.RawPath = ""
	query := url.Values{}
	query.Set("model", p.Config.Model)
	base.RawQuery = query.Encode()
	base.Fragment = ""
	req, err := http.NewRequestWithContext(checkCtx, "GET", base.String(), nil)
	if err != nil {
		return false, 0
	}
	req.Header.Set("loop", "v1.0.0")
	if key := p.requestKey(ctx); key != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))
	}
	client := p.HttpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
		if readErr == nil && len(bodyBytes) > 0 {
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
			if json.Unmarshal(bodyBytes, &props) == nil {
				supported := false
				for _, flag := range []json.RawMessage{props.SupportsReasoning, props.SupportsThinking, props.Thinking} {
					var enabled bool
					if json.Unmarshal(flag, &enabled) == nil && enabled {
						supported = true
					}
				}
				detected := props.NCtx
				if detected <= 0 {
					detected = props.MaxContext
				}
				if detected <= 0 {
					detected = props.EngineMaxContext
				}
				if detected <= 0 {
					detected = props.DefaultGenerationSettings.NCtx
				}
				if detected <= 0 {
					detected = props.DefaultGenerationSettings.MaxContext
				}
				return supported, detected
			}
		}
		return false, 0
	}
	return false, 0
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
