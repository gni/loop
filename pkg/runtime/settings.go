package runtime

import (
	"encoding/json"
	"fmt"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/model"
	"loop/pkg/domain/session"
)

type savedSettings struct {
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Reasoning     string          `json:"reasoning"`
	DisabledTools []string        `json:"disabled_tools"`
	Tools         []string        `json:"tools"`
	Todos         []tool.TodoItem `json:"todos"`
}

func restoreSettings(store *db.JSONLStore, id string, cfg *config.Config, tools *[]string, keepSelection bool) ([]tool.TodoItem, error) {
	if !store.HasMessages(id) {
		return nil, nil
	}
	entries, err := store.Entries(id)
	if err != nil {
		return nil, err
	}
	var todos []tool.TodoItem
	restoreTools := *tools == nil
	for _, entry := range session.LeafPath(entries, "") {
		if entry.Type != "settings" {
			continue
		}
		var saved savedSettings
		if err := json.Unmarshal(entry.Data, &saved); err != nil {
			return nil, fmt.Errorf("session settings: %w", err)
		}
		if !keepSelection {
			if saved.Provider != "" {
				if err := cfg.ActivateProvider(saved.Provider); err != nil {
					return nil, err
				}
			}
			if saved.Model != "" {
				cfg.Model = saved.Model
			}
			cfg.ReasoningEffort = saved.Reasoning
			cfg.DisabledTools = saved.DisabledTools
		}
		if restoreTools {
			*tools = saved.Tools
		}
		todos = saved.Todos
	}
	return todos, nil
}

func (s *Session) persistSettings() error {
	if s.Store == nil {
		return nil
	}
	cfg := s.Agent.Config
	data, err := json.Marshal(savedSettings{Provider: cfg.ActiveProvider, Model: cfg.Model, Reasoning: cfg.ReasoningEffort, DisabledTools: cfg.DisabledTools, Tools: s.options.Tools, Todos: s.Agent.GetTodos()})
	if err != nil {
		return err
	}
	return s.Store.AppendEntry(s.ID, session.Record{Type: "settings", Data: data})
}

func (s *Session) Configuration() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return config.Clone(s.Agent.Config)
}

func (s *Session) SelectModel(provider, query string) error {
	cfg, err := s.Configuration()
	if err != nil {
		return err
	}
	entry, err := model.Resolve(cfg.Models(), provider, query)
	if err != nil {
		return err
	}
	if err := cfg.ActivateProvider(entry.Provider); err != nil {
		return err
	}
	cfg.Model = entry.ID
	if entry.ContextWindow > 0 {
		cfg.ContextWindowLimit = entry.ContextWindow
	}
	if entry.MaxOutput > 0 && cfg.MaxCompletionTokens > entry.MaxOutput {
		cfg.MaxCompletionTokens = entry.MaxOutput
	}
	return s.UpdateConfig(cfg)
}
