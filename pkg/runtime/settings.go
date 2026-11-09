package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

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
	return restoreSettingsContext(context.Background(), store, id, cfg, tools, keepSelection)
}

func restoreSettingsContext(ctx context.Context, store *db.JSONLStore, id string, cfg *config.Config, tools *[]string, keepSelection bool) ([]tool.TodoItem, error) {
	if !store.HasMessages(id) {
		return nil, nil
	}
	entries, err := store.EntriesContext(ctx, id)
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
	return s.persistSettingsIfActiveContext(s.Agent.Context())
}

func (s *Session) persistSettingsIfActiveContext(ctx context.Context) error {
	if s.Store == nil {
		return nil
	}
	if !s.Store.HasMessages(s.ID) && len(s.Agent.GetTodos()) == 0 {
		return nil
	}
	return s.persistSettingsForContext(ctx, s.ID)
}

func (s *Session) persistSettingsFor(id string) error {
	return s.persistSettingsForContext(s.Agent.Context(), id)
}

func (s *Session) persistSettingsForContext(ctx context.Context, id string) error {
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
	if err := s.Agent.SavePendingSessionName(ctx, id); err != nil {
		return err
	}
	s.Agent.LLMProviderMu.Lock()
	cfg := s.Agent.Config
	saved := savedSettings{Provider: cfg.ActiveProvider, Model: cfg.Model, Reasoning: cfg.ReasoningEffort, DisabledTools: slices.Clone(cfg.DisabledTools)}
	s.Agent.LLMProviderMu.Unlock()
	saved.Tools, saved.Todos = slices.Clone(s.options.Tools), s.Agent.GetTodos()
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return s.Store.AppendEntryContext(ctx, id, session.Record{Type: "settings", Data: data})
}

func sessionNameFromEntries(entries []session.Record) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "name" {
			return entries[i].Name
		}
	}
	return ""
}

func (s *Session) checkpointMessages(ctx context.Context) error {
	if s.Store == nil {
		return nil
	}
	messages := s.MessagesSnapshot()
	return s.Agent.CheckpointSession(ctx, s.ID, messages)
}

// Configuration returns an independent snapshot under the agent's config lock.
func (s *Session) Configuration() (*config.Config, error) {
	return s.Agent.Configuration()
}
