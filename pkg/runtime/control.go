package runtime

import (
	"context"
	"errors"
	"io"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (s *Session) SetName(ctx context.Context, name string) error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	return s.Agent.SetSessionName(ctx, name)
}

func (s *Session) UpdateConfig(cfg *config.Config) error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	applyErr := s.Agent.ApplyConfig(cfg)
	if !agent.ConfigApplied(applyErr) {
		return applyErr
	}
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	if err := errors.Join(applyErr, s.persistSettings(), s.Agent.PersistenceError()); err != nil {
		return &agent.ConfigApplyError{Err: err}
	}
	return nil
}

var _ agent.ConfigurationController = (*Session)(nil)

func (s *Session) SetTools(names []string) error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	if err := s.validateTools(names); err != nil {
		return err
	}
	s.options.Tools = append([]string{}, names...)
	s.Agent.ToolSelection = s.options.Tools
	return s.persistSettings()
}

func (s *Session) Compact(ctx context.Context) error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	if err := s.Agent.CompressHistory(ctx, &s.messages, s.storageID(), style.GetTheme("plain"), io.Discard); err != nil {
		return err
	}
	return nil
}

// SyncInteractive makes local session changes effective immediately, including
// when the user switches sessions and exits without sending another prompt.
func (s *Session) SyncInteractive(messages []db.Message, tools []string, id string, inheritPlan bool) error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	if inheritPlan && id != "" && id != s.ID {
		if err := s.persistSettingsFor(id); err != nil {
			return err
		}
	}
	if err := s.adoptInteractive(messages, tools, id); err != nil {
		return err
	}
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	return s.persistSettingsIfActive()
}
