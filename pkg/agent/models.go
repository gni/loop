package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"loop/pkg/db"
	"loop/pkg/domain/model"
	"net/http"
	"os"
	"strings"
	"time"
)

func (p *OpenAICompatibleProvider) selectedModel() model.Model {
	if entry, err := model.Resolve(p.Config.Models(), p.Config.ActiveProvider, p.Config.Model); err == nil {
		return entry
	}
	return model.Model{ID: p.Config.Model, Provider: p.Config.ActiveProvider}
}

func (p *OpenAICompatibleProvider) validateCapabilities(messages []db.Message) error {
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
	profile := a.Config.Providers[a.Config.ActiveProvider]
	base := strings.TrimRight(a.Config.Endpoint, "/")
	endpoint := base + "/models"
	if !strings.HasSuffix(base, "/v1") && !strings.HasSuffix(base, "/v1beta") {
		endpoint = base + "/v1/models"
	}
	if profile.API == "generate-content" && !strings.HasSuffix(base, "/v1") && !strings.HasSuffix(base, "/v1beta") {
		endpoint = base + "/v1beta/models"
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
	switch profile.API {
	case "messages":
		request.Header.Set("x-api-key", key)
		request.Header.Set("anthropic-version", "2023-06-01")
	case "generate-content":
		request.Header.Set("x-goog-api-key", key)
	default:
		if key != "" {
			request.Header.Set("Authorization", "Bearer "+key)
		}
	}
	for name, value := range profile.Headers {
		request.Header.Set(name, os.ExpandEnv(value))
	}
	response, err := a.HttpClient.Do(request)
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
