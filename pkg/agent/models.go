package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/auth"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/model"
	"loop/pkg/resources"
	transporthttp "loop/pkg/transport/http"
)

// DiscoverModels queries provider metadata without constructing a session,
// loading project resources or starting executable integrations.
func DiscoverModels(ctx context.Context, cfg *config.Config, configPath string) ([]model.Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("provider configuration unavailable")
	}
	tlsConfig, err := config.GetTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
		transport = base.Clone()
	}
	transport.TLSClientConfig = tlsConfig
	defer transport.CloseIdleConnections()
	a := &Agent{Config: cfg, ConfigPath: configPath, Credentials: auth.NewStore(filepath.Join(resources.AgentDirectory(configPath), "auth.json"))}
	a.HTTPClient = &http.Client{Transport: transport}
	return a.DiscoverModels(ctx)
}

func (p *OpenAICompatibleProvider) selectedModel() model.Model {
	if entry, err := model.Resolve(p.Config.Models(), p.Config.ActiveProvider, p.Config.Model); err == nil {
		return entry
	}
	return model.Model{ID: p.Config.Model, Provider: p.Config.ActiveProvider}
}

func (p *OpenAICompatibleProvider) validateCapabilities(messages []db.Message) error {
	if p == nil || p.Config == nil {
		return fmt.Errorf("provider config is nil")
	}
	selected := p.selectedModel()
	for _, msg := range messages {
		for _, block := range msg.ContentBlocks {
			if block.Type == "image" && !selected.Supports("image") {
				return fmt.Errorf("model %s does not support image input", selected.ID)
			}
		}
	}
	return nil
}

func (a *Agent) DiscoverModels(ctx context.Context) ([]model.Model, error) {
	if a == nil {
		return nil, fmt.Errorf("provider config is nil")
	}
	a.LLMProviderMu.RLock()
	cfg, err := config.Clone(a.Config)
	client := a.HTTPClient
	a.LLMProviderMu.RUnlock()
	if err != nil {
		return nil, err
	}
	profile := cfg.Providers[cfg.ActiveProvider]
	endpoint, err := transporthttp.ModelDiscoveryURL(cfg.Endpoint, profile.API)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, transporthttp.RedactError(err)
	}
	key, err := a.resolveCredentialForConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	transporthttp.ApplyProviderAuthHeaders(request.Header, profile.API, key)
	transporthttp.ApplyProviderHeaders(request.Header, profile.Headers)
	if client == nil {
		client = http.DefaultClient
	}
	// Custom credential headers must never follow a discovery redirect to
	// another origin. Use the configured API endpoint directly.
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := requestClient.Do(request)
	if err != nil {
		return nil, transporthttp.RedactError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model discovery returned HTTP %d", response.StatusCode)
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name   string `json:"name"`
			Input  int    `json:"inputTokenLimit"`
			Output int    `json:"outputTokenLimit"`
		} `json:"models"`
	}
	const maxDiscoveryBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDiscoveryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model discovery: %w", err)
	}
	if len(body) > maxDiscoveryBytes {
		return nil, fmt.Errorf("model discovery exceeds 4 MiB")
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(body)) == "null" {
		return nil, fmt.Errorf("model discovery must return a JSON object")
	}
	entries := cfg.Models()
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Provider == cfg.ActiveProvider {
			seen[entry.ID] = true
		}
	}
	for _, entry := range data.Data {
		if strings.TrimSpace(entry.ID) != "" && !seen[entry.ID] {
			entries = append(entries, model.Model{ID: entry.ID, Provider: cfg.ActiveProvider, API: profile.API})
			seen[entry.ID] = true
		}
	}
	for _, entry := range data.Models {
		id := strings.TrimPrefix(entry.Name, "models/")
		if strings.TrimSpace(id) != "" && !seen[id] {
			entries = append(entries, model.Model{ID: id, Provider: cfg.ActiveProvider, API: profile.API, ContextWindow: entry.Input, MaxOutput: entry.Output})
			seen[id] = true
		}
	}
	return entries, nil
}
