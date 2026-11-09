package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loop/pkg/db"
	"loop/pkg/domain/model"
	transporthttp "loop/pkg/transport/http"
)

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
	if a == nil || a.Config == nil {
		return nil, fmt.Errorf("provider config is nil")
	}
	profile := a.Config.Providers[a.Config.ActiveProvider]
	endpoint, err := transporthttp.ModelDiscoveryURL(a.Config.Endpoint, profile.API)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	key, err := a.resolveCredential(ctx)
	if err != nil {
		return nil, err
	}
	transporthttp.ApplyProviderAuthHeaders(request.Header, profile.API, key)
	transporthttp.ApplyProviderHeaders(request.Header, profile.Headers)
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
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
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&data); err != nil {
		return nil, err
	}
	entries := a.Config.Models()
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Provider == a.Config.ActiveProvider {
			seen[entry.ID] = true
		}
	}
	for _, entry := range data.Data {
		if !seen[entry.ID] {
			entries = append(entries, model.Model{ID: entry.ID, Provider: a.Config.ActiveProvider, API: profile.API})
			seen[entry.ID] = true
		}
	}
	for _, entry := range data.Models {
		id := strings.TrimPrefix(entry.Name, "models/")
		if !seen[id] {
			entries = append(entries, model.Model{ID: id, Provider: a.Config.ActiveProvider, API: profile.API, ContextWindow: entry.Input, MaxOutput: entry.Output})
			seen[id] = true
		}
	}
	return entries, nil
}
