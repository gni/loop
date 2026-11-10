package runtime

import (
	"context"
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
	for _, hook := range hooks {
		if hook.Event == event.Type && hook.Event != "tool_call" && hook.Event != "tool_result" {
			if _, err := resources.RunHook(ctx, hook, event); err != nil {
				return err
			}
		}
	}
	if s.options.Events != nil {
		return s.options.Events(event)
	}
	return nil
}
