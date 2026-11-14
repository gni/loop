package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/resources"
)

func (s *Session) event(event agent.Event) error {
	ctx := s.Agent.Context()
	if event.Type == "session_end" || event.Type == "agent_end" {
		ctx = context.WithoutCancel(ctx)
	}
	return s.eventContext(ctx, event)
}

func (s *Session) eventContext(ctx context.Context, event agent.Event) error {
	// A plan update must survive interruption before the model turn ends.
	if event.Type == "tool_execution_end" && event.ToolCall != nil && event.ToolCall.Function.Name == "todo" && !event.IsError && !strings.HasPrefix(event.SessionID, "subagent:") {
		if err := s.persistSettings(); err != nil {
			return err
		}
	}
	event.Version = 1
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if event.SessionID == "" {
		event.SessionID = s.ID
	}
	s.mu.Lock()
	hooks := append([]resources.Hook(nil), s.hooks...)
	s.mu.Unlock()
	var hookErr error
	var stopHooks context.CancelFunc
	for _, hook := range hooks {
		if hook.Event == event.Type && hook.Event != "tool_call" && hook.Event != "tool_result" {
			if stopHooks == nil {
				ctx, stopHooks = s.eventHookContext(ctx, event.Type)
				defer stopHooks()
			}
			if err := s.runEventHook(ctx, hook, event); err != nil {
				hookErr = err
				break
			}
		}
	}
	if hookErr != nil && !terminalEvent(event.Type) {
		return hookErr
	}
	if s.options.Events != nil {
		return errors.Join(hookErr, s.options.Events(event))
	}
	return hookErr
}

func terminalEvent(eventType string) bool {
	return eventType == "agent_end" || eventType == "agent_settled" || eventType == "session_end"
}

func (s *Session) eventHookContext(ctx context.Context, eventType string) (context.Context, context.CancelFunc) {
	var stopDeadline context.CancelFunc = func() {}
	if terminalEvent(eventType) {
		ctx, stopDeadline = context.WithTimeout(ctx, 2*time.Second)
	}
	ctx, cancel := context.WithCancel(ctx)
	stopLifetime := func() bool { return false }
	if lifetime := s.Agent.Lifetime; eventType != "session_end" && lifetime != nil {
		stopLifetime = context.AfterFunc(lifetime, cancel)
		if lifetime.Err() != nil {
			cancel()
		}
	}
	return ctx, func() { stopLifetime(); cancel(); stopDeadline() }
}

func (s *Session) runEventHook(ctx context.Context, hook resources.Hook, event agent.Event) error {
	if event.Type != "session_end" {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return context.Canceled
		}
		s.eventHooks.Add(1)
		s.mu.Unlock()
		defer s.eventHooks.Done()
	}
	_, err := resources.RunHook(ctx, hook, event)
	return err
}
