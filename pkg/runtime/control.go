package runtime

import (
	"context"
	"fmt"
	"io"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

func (s *Session) UpdateConfig(cfg *config.Config) error {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	defer s.runMu.Unlock()
	copy, err := config.Clone(cfg)
	if err != nil {
		return err
	}
	if err := copy.ValidatePrompt(); err != nil {
		return err
	}
	if err := copy.Nostop.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("session is closed")
	}
	s.mu.Lock()
	s.Agent.ApplyConfig(copy)
	s.mu.Unlock()
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	if err := s.persistSettings(); err != nil {
		return err
	}
	s.mu.Lock()
	s.view = append(s.view[:0], s.messages...)
	s.mu.Unlock()
	return s.Agent.PersistenceError()
}

func (s *Session) SetTools(names []string) error {
	if !s.runMu.TryLock() {
		return ErrBusy
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
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	defer s.runMu.Unlock()
	if err := s.Agent.CompressHistory(ctx, &s.messages, s.storageID(), style.GetTheme("plain"), io.Discard); err != nil {
		return err
	}
	s.mu.Lock()
	s.view = append(s.view[:0], s.messages...)
	s.mu.Unlock()
	return nil
}

// SyncInteractive makes local session changes effective immediately, including
// when the user switches sessions and exits without sending another prompt.
func (s *Session) SyncInteractive(messages []db.Message, tools []string, id string, inheritPlan bool) error {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	defer s.runMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("session is closed")
	}
	if inheritPlan && id != "" && id != s.ID {
		if err := s.persistSettingsFor(id); err != nil {
			return err
		}
	}
	if err := s.adoptInteractive(messages, tools, id); err != nil {
		return err
	}
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	s.mu.Lock()
	s.view = message.CloneAll(s.messages)
	s.mu.Unlock()
	return s.persistSettings()
}
