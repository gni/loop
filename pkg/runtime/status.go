package runtime

import "loop/pkg/domain/message"

// Running checks execution state without copying queues or conversation data.
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Status describes the session without including its conversation history.
func (s *Session) Status() State {
	s.mu.Lock()
	state := State{ID: s.ID, Workspace: s.Workspace, Running: s.running, Closed: s.closed,
		Steering: append([]string(nil), s.steering...), FollowUp: append([]string(nil), s.followUp...)}
	state.Name, _ = s.Agent.SessionNameSnapshot()
	s.mu.Unlock()
	s.Agent.LLMProviderMu.RLock()
	state.Model, state.Provider = s.Agent.Config.Model, s.Agent.Config.ActiveProvider
	s.Agent.LLMProviderMu.RUnlock()
	state.Todos = s.Agent.GetTodos()
	return state
}

// State retains the full snapshot for callers explicitly loading a conversation.
func (s *Session) State() State {
	state := s.Status()
	state.Messages = s.MessagesSnapshot()
	return state
}

// Usage totals recorded usage without cloning message bodies or attachments.
func (s *Session) Usage() message.Usage {
	s.Agent.MessagesMu.RLock()
	defer s.Agent.MessagesMu.RUnlock()
	return message.TotalUsage(s.messages)
}
