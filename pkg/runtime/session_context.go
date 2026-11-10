package runtime

import (
	"context"
	"path/filepath"
	"slices"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	domainsession "loop/pkg/domain/session"
	"loop/pkg/resources"
)

func (s *Session) adoptInteractive(messages []db.Message, tools []string, id string) error {
	return s.adoptInteractiveContext(s.Agent.Context(), messages, tools, id)
}

func (s *Session) adoptInteractiveContext(ctx context.Context, messages []db.Message, tools []string, id string) error {
	if id != "" {
		if err := domainsession.ValidateID(id); err != nil {
			return err
		}
	}
	switching := id != "" && id != s.ID
	// Accepted foreground history still belongs to the current session during
	// cancellation. Switching resources requires a live operation context.
	if switching {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var restoredName string
	if switching {
		if s.Agent.PersistenceError() != nil {
			if err := s.checkpointMessages(ctx); err != nil {
				return err
			}
		}
		if err := s.persistSettingsIfActiveContext(ctx); err != nil {
			return err
		}
		cfg, err := s.Configuration()
		if err != nil {
			return err
		}
		var todos []tool.TodoItem
		if s.Store != nil {
			var entries []domainsession.Record
			if s.Store.HasMessages(id) {
				entries, err = s.Store.EntriesContext(ctx, id)
				if err != nil {
					return err
				}
			}
			todos, err = restoreSettingsFromEntries(entries, cfg, &tools, false)
			if err != nil {
				return err
			}
			restoredName = sessionNameFromEntries(entries)
		}
		prepared, err := s.Manager.PrepareSessionDirectory(filepath.Join(resources.AgentDirectory(s.Agent.ConfigPath), "agents", config.WorkspaceKey(s.Workspace), id), s.options.NoSession)
		if err != nil {
			return err
		}
		defer prepared.Close()
		applyErr := s.Agent.ApplyConfig(cfg)
		if !agent.ConfigApplied(applyErr) {
			return applyErr
		}
		if applyErr != nil {
			s.Agent.SetPendingSystemEvent(applyErr.Error())
		}
		if err := prepared.Commit(); err != nil {
			return err
		}
		if err := s.Agent.SetTodos(todos); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		s.ID = id
	}
	s.Agent.SetActiveSessionID(s.ID)
	if switching {
		s.Agent.RestoreSessionName(s.ID, restoredName)
	}
	s.Agent.MessagesMu.Lock()
	s.messages = message.CloneAll(messages)
	s.Agent.MessagesMu.Unlock()
	s.options.Tools = slices.Clone(tools)
	s.Agent.ToolSelection = s.options.Tools
	return nil
}
