package runtime

import (
	"encoding/json"
	"fmt"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
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
	return restoreSettingsFromEntries(entries, cfg, tools, keepSelection)
}

func restoreSettingsFromEntries(entries []session.Record, cfg *config.Config, tools *[]string, keepSelection bool) ([]tool.TodoItem, error) {
	path := session.LeafPath(entries, "")
	todos, err := restorePlan(path)
	if err != nil {
		return nil, err
	}
	restoreTools := *tools == nil
	for _, entry := range path {
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
	}
	return todos, nil
}

func (s *Session) persistSettings() error { return s.persistSettingsFor(s.ID) }

func (s *Session) persistSettingsIfActive() error {
	if s.Store == nil {
		return nil
	}
	if !s.Store.HasMessages(s.ID) && len(s.Agent.GetTodos()) == 0 {
		return nil
	}
	return s.persistSettingsFor(s.ID)
}

func (s *Session) persistSettingsFor(id string) error {
	if s.Store == nil {
		return nil
	}
	// Settings are kept in memory until the conversation has actual content.
	// UI commands and plan/config changes alone must not create session files.
	s.Agent.MessagesMu.RLock()
	active := false
	for _, msg := range s.messages {
		if msg.HasConversationContent() {
			active = true
			break
		}
	}
	s.Agent.MessagesMu.RUnlock()
	if !active {
		return nil
	}
	cfg := s.Agent.Config
	data, err := json.Marshal(savedSettings{Provider: cfg.ActiveProvider, Model: cfg.Model, Reasoning: cfg.ReasoningEffort, DisabledTools: cfg.DisabledTools, Tools: s.options.Tools, Todos: s.Agent.GetTodos()})
	if err != nil {
		return err
	}
	return s.Store.AppendEntry(id, session.Record{Type: "settings", Data: data})
}

func (s *Session) Configuration() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return config.Clone(s.Agent.Config)
}
