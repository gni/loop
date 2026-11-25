package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/ui/style"
)

type Outcome struct {
	Result agent.RunResult
	Err    error
}

// lockIdle reserves an open, idle session for a mutation. The caller must unlock runMu.
func (s *Session) lockIdle() error {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.runMu.Unlock()
		return fmt.Errorf("session is closed")
	}
	return nil
}

func (s *Session) reserve(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.runMu.TryLock() {
		return nil, ErrBusy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Checking closure and publishing the running state must be atomic with Close.
	if s.closed {
		s.runMu.Unlock()
		return nil, fmt.Errorf("session is closed")
	}
	if err := ctx.Err(); err != nil {
		s.runMu.Unlock()
		return nil, err
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.idle = make(chan struct{})
	return ctx, nil
}

// releaseRun completes the reservation shared by inference and commands.
func (s *Session) releaseRun() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	s.cancel = nil
	idle := s.idle
	s.mu.Unlock()
	s.runMu.Unlock()
	close(idle)
}

func (s *Session) Start(ctx context.Context, prompt string) (<-chan Outcome, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return nil, err
	}
	done := make(chan Outcome, 1)
	go func() {
		result, err := s.run(ctx, prompt)
		done <- Outcome{Result: result, Err: err}
		close(done)
	}()
	return done, nil
}

func (s *Session) Run(ctx context.Context, prompt string) (agent.RunResult, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return agent.RunResult{}, err
	}
	return s.run(ctx, prompt)
}

func (s *Session) run(ctx context.Context, prompt string) (agent.RunResult, error) {
	var output io.Writer = io.Discard
	s.Agent.LLMProviderMu.RLock()
	nostop := s.Agent.Config != nil && s.Agent.Config.Nostop.Enabled()
	s.Agent.LLMProviderMu.RUnlock()
	if nostop && s.options.Output != nil {
		output = s.options.Output
	}
	return s.runWith(ctx, prompt, output, style.GetTheme("plain"), true)
}

func (s *Session) RunInteractive(ctx context.Context, w io.Writer, messages *[]db.Message, prompt string, tools []string, theme style.UITheme, id string) (agent.RunResult, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return agent.RunResult{}, err
	}
	result, err := s.runPrepared(ctx, prompt, w, theme, false, func() error { return s.adoptInteractiveContext(ctx, *messages, tools, id) })
	*messages = s.MessagesSnapshot()
	return result, err
}

func (s *Session) runWith(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool) (agent.RunResult, error) {
	return s.runPrepared(ctx, prompt, w, theme, noninteractive, nil)
}

func (s *Session) runPrepared(ctx context.Context, prompt string, w io.Writer, theme style.UITheme, noninteractive bool, setup func() error) (result agent.RunResult, runErr error) {
	runID := s.ID
	defer func() {
		if err := s.persistSettingsIfActiveContext(ctx); err != nil {
			runErr = errors.Join(runErr, err)
		}
		result = result.WithErrorContext(ctx, runErr)
		snapshot := result.Clone()
		s.releaseRun()
		if err := s.eventContext(context.WithoutCancel(ctx), agent.Event{Type: "agent_settled", SessionID: runID, Result: &snapshot}); err != nil {
			runErr = errors.Join(runErr, err)
			result = result.WithErrorContext(context.WithoutCancel(ctx), runErr)
		}
	}()
	if setup != nil {
		if err := setup(); err != nil {
			return result, err
		}
		runID = s.ID
	}
	for {
		if err := ctx.Err(); err != nil {
			return agent.RunResult{Status: agent.RunCancelled, Error: err.Error()}, err
		}
		attachmentSource, literal := resources.AttachmentSource(ctx, prompt)
		preExpanded := resources.IsPreExpandedPrompt(ctx, prompt)
		attached, blocks, err := resources.AttachmentsContext(ctx, attachmentSource, s.Workspace, s.Agent.SafePath)
		if err != nil {
			return result, err
		}
		attachmentSuffix := strings.TrimPrefix(attached, attachmentSource)

		transformed, err := s.transformInput(ctx, prompt)
		if err != nil {
			result.Status = agent.RunBlocked
			result.Error = err.Error()
			return result, err
		}
		expanded := transformed
		if !preExpanded && (!literal || strings.HasPrefix(strings.TrimSpace(attachmentSource), "/")) {
			s.Agent.LLMProviderMu.RLock()
			projectResources := s.Agent.Config.ProjectResources
			promptDirs := slices.Clone(s.Agent.Config.PromptDirectories)
			s.Agent.LLMProviderMu.RUnlock()
			expanded, err = resources.ExpandPrompt(transformed, resources.PromptDirectories(resources.AgentDirectory(s.Agent.ConfigPath), s.Workspace, projectResources, promptDirs))
			if err != nil {
				return result, err
			}
		}
		expanded += attachmentSuffix
		s.Agent.InputBlocks = blocks
		tools := slices.Clone(s.options.Tools)
		s.Agent.LLMProviderMu.Lock()
		s.Agent.ToolSelection = tools
		s.Agent.LLMProviderMu.Unlock()
		result, err = s.Agent.RunAgentLoop(ctx, w, &s.messages, expanded, tools, theme, noninteractive, s.storageID())
		if err != nil {
			return result, err
		}
		s.mu.Lock()
		if len(s.steering) > 0 {
			prompt = s.steering[0]
			s.steering[0] = ""
			s.steering = s.steering[1:]
			if len(s.steering) == 0 {
				s.steering = nil
			}
		} else if len(s.followUp) > 0 {
			prompt = s.followUp[0]
			s.followUp[0] = ""
			s.followUp = s.followUp[1:]
			if len(s.followUp) == 0 {
				s.followUp = nil
			}
		} else {
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		if err := s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.Status()}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Session) Queue(prompt string, followUp bool) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("queued prompt is empty")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	if len(s.steering)+len(s.followUp) >= 256 {
		s.mu.Unlock()
		return fmt.Errorf("input queue is full")
	}
	if followUp {
		s.followUp = append(s.followUp, prompt)
	} else {
		s.steering = append(s.steering, prompt)
	}
	s.mu.Unlock()
	return s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.Status()})
}

func (s *Session) takeSteering() []db.Message {
	s.mu.Lock()
	if len(s.steering) == 0 {
		s.mu.Unlock()
		return nil
	}
	prompt := s.steering[0]
	s.steering[0] = ""
	s.steering = s.steering[1:]
	if len(s.steering) == 0 {
		s.steering = nil
	}
	s.mu.Unlock()
	_ = s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.Status()})
	return []db.Message{{Role: "user", Content: prompt}}
}

func (s *Session) ClearQueue() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	s.steering = nil
	s.followUp = nil
	s.mu.Unlock()
	return s.event(agent.Event{Type: "queue_update", SessionID: s.ID, Data: s.Status()})
}
