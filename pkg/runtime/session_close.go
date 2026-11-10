package runtime

import (
	"context"
	"errors"
	"time"

	"loop/pkg/agent"
)

func (s *Session) Wait(ctx context.Context) error {
	s.mu.Lock()
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) Abort(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.Wait(ctx)
}

// Close starts cleanup once. A caller timing out does not abandon cleanup,
// and later callers can still wait for its completion and receive its result.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closeDone == nil {
		s.closed = true
		s.closeDone = make(chan struct{})
		cancel := s.cancel
		go s.finishClose()
		if cancel != nil {
			cancel()
		}
	}
	done := s.closeDone
	s.mu.Unlock()
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	default:
	}
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) finishClose() {
	if s.Agent.CancelLifetime != nil {
		s.Agent.CancelLifetime()
	}
	_ = s.Wait(context.Background())
	s.runMu.Lock()
	// Storage contention must not keep processes alive or block exit forever.
	disposeErr := s.Agent.Dispose(context.Background())
	flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	var checkpointErr error
	if s.Agent.PersistenceError() != nil {
		checkpointErr = s.checkpointMessages(flushCtx)
	}
	settingsErr := s.persistSettingsIfActiveContext(flushCtx)
	cancel()
	s.runMu.Unlock()
	// A blocked event consumer must not keep processes or connections alive.
	err := errors.Join(checkpointErr, settingsErr, disposeErr, s.event(agent.Event{Type: "session_end", SessionID: s.ID}))
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
}
