package runtime

import (
	"context"
	"fmt"
	"io"

	"loop/pkg/config"
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
