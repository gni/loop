package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (p *OpenAICompatibleProvider) GetDetectedContextLimit() int {
	p.ContextLimitMu.RLock()
	defer p.ContextLimitMu.RUnlock()
	return p.DetectedContextLimit
}

func (p *OpenAICompatibleProvider) CheckThinkingSupport(ctx context.Context) bool {
	p.thinkingMu.Lock()
	defer p.thinkingMu.Unlock()
	return p.checkThinkingSupportLocked(ctx)
}

// checkThinkingSupportLocked performs the probe assuming thinkingMu is already held.
func (p *OpenAICompatibleProvider) checkThinkingSupportLocked(ctx context.Context) bool {
	timeout := 5 * time.Second
	if p.Config != nil && p.Config.Timeout > 0 && time.Duration(p.Config.Timeout)*time.Second < timeout {
		timeout = time.Duration(p.Config.Timeout) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("%s/props?model=%s", strings.TrimSuffix(p.Config.Endpoint, "/"), p.Config.Model)
	req, err := http.NewRequestWithContext(checkCtx, "GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("loop", "v1.0.0")
	if p.Config.ApiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
	}
	client := p.HttpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
		if readErr == nil && len(bodyBytes) > 0 {
			var props struct {
				NCtx                      int `json:"n_ctx"`
				MaxContext                int `json:"max_context"`
				EngineMaxContext          int `json:"engine_max_context"`
				DefaultGenerationSettings struct {
					NCtx       int `json:"n_ctx"`
					MaxContext int `json:"max_context"`
				} `json:"default_generation_settings"`
			}
			if json.Unmarshal(bodyBytes, &props) == nil {
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
				if detected > 0 {
					p.ContextLimitMu.Lock()
					p.DetectedContextLimit = detected
					p.ContextLimitChecked = true
					p.ContextLimitMu.Unlock()
				}
			}
		}
		return true
	}
	return false
}

// ProbeServerCapabilities queries the backend (/props) to discover
// reasoning support and the server's native active context window limit.
func (p *OpenAICompatibleProvider) ProbeServerCapabilities(ctx context.Context) {
	p.thinkingMu.Lock()
	p.ThinkingSupported = p.checkThinkingSupportLocked(ctx)
	p.ThinkingSupportChecked = true
	p.thinkingMu.Unlock()
	p.ContextLimitMu.Lock()
	p.ContextLimitChecked = true
	p.ContextLimitMu.Unlock()
}
