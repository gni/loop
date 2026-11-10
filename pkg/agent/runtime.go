package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"loop/pkg/agent/run"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/task"
)

type RunStatus = run.Status

const (
	RunCompleted    RunStatus = run.Completed
	RunFailed       RunStatus = run.Failed
	RunCancelled    RunStatus = run.Cancelled
	RunBlocked      RunStatus = run.Blocked
	RunLimitReached RunStatus = run.LimitReached
)

type RunResult struct {
	TaskState *task.State  `json:"task_state,omitempty"`
	Status    RunStatus    `json:"status"`
	Text      string       `json:"text,omitempty"`
	Turns     int          `json:"turns"`
	Messages  []db.Message `json:"messages,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// Clone returns a result whose messages and task state can be changed independently.
func (r RunResult) Clone() RunResult {
	r.TaskState = task.Clone(r.TaskState)
	r.Messages = message.CloneAll(r.Messages)
	return r
}

// WithError records a failure, preserving specific stop reasons and classifying
// both explicit cancellation and deadlines as cancelled runs.
func (r RunResult) WithError(err error) RunResult {
	if err == nil {
		return r
	}
	r.Error = err.Error()
	if r.Status == RunCompleted || r.Status == "" {
		r.Status = RunFailed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.Status = RunCancelled
	}
	return r
}

type Event struct {
	Version   int          `json:"version"`
	Type      string       `json:"type"`
	SessionID string       `json:"session_id,omitempty"`
	Time      time.Time    `json:"time"`
	Message   *db.Message  `json:"message,omitempty"`
	Delta     string       `json:"delta,omitempty"`
	Kind      string       `json:"kind,omitempty"`
	ToolCall  *db.ToolCall `json:"tool_call,omitempty"`
	Output    string       `json:"output,omitempty"`
	IsError   bool         `json:"is_error,omitempty"`
	Result    *RunResult   `json:"result,omitempty"`
	Data      any          `json:"data,omitempty"`
}

func (a *Agent) emit(event Event) {
	if err := a.sendEvent(event); err != nil {
		a.recordPersistenceError(fmt.Errorf("event output: %w", err))
	}
}

func (a *Agent) sendEvent(event Event) error {
	if a.EventSink == nil {
		return nil
	}
	event.Version = 1
	event.Time = time.Now().UTC()
	if event.Message != nil {
		snapshot := message.Clone(*event.Message)
		event.Message = &snapshot
	}
	if event.ToolCall != nil {
		snapshot := *event.ToolCall
		if snapshot.Index != nil {
			index := *snapshot.Index
			snapshot.Index = &index
		}
		event.ToolCall = &snapshot
	}
	if event.Result != nil {
		snapshot := event.Result.Clone()
		event.Result = &snapshot
	}
	return a.EventSink(event)
}

func (a *Agent) recordPersistenceError(err error) {
	if err == nil {
		return
	}
	a.stateMu.Lock()
	a.persistenceFailures++
	if a.persistenceFirstErr == nil {
		a.persistenceFirstErr = err
	}
	if a.persistenceFailures == 1 {
		a.persistenceErr = err
	} else {
		// Retain the first and latest causes without nesting the entire history.
		a.persistenceErr = fmt.Errorf("%d persistence/output failures; first: %w; latest: %w", a.persistenceFailures, a.persistenceFirstErr, err)
	}
	a.stateMu.Unlock()
}

func (a *Agent) persistenceError() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.persistenceErr
}

func (a *Agent) Dispose(ctx context.Context) error {
	if a.CancelLifetime != nil {
		a.CancelLifetime()
	}
	a.KillAllTasks()
	a.StopMCPServers()
	if a.DisposeAgents != nil {
		a.DisposeAgents()
	}
	if a.DebugLogger != nil {
		a.DebugLogger.Close()
	}
	if a.HTTPClient != nil {
		a.HTTPClient.CloseIdleConnections()
	}
	return ctx.Err()
}

func (a *Agent) PersistenceError() error { return a.persistenceError() }

func (a *Agent) resolveCredential(ctx context.Context) (string, error) {
	return a.resolveCredentialForConfig(ctx, a.Config)
}

func (a *Agent) resolveCredentialForConfig(ctx context.Context, cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("provider configuration unavailable")
	}
	profile, exists := cfg.Providers[cfg.ActiveProvider]
	if !exists {
		profile = config.ProviderConfig{Endpoint: cfg.Endpoint, APIKey: cfg.APIKey}
	}
	profile.Endpoint = cfg.Endpoint
	profile.APIKey = cfg.APIKey
	if a.Credentials == nil {
		return profile.ResolvedAPIKey(), profile.AuthStatus()
	}
	return a.Credentials.Resolve(ctx, cfg.ActiveProvider, profile)
}

// PageRecentMessages selects recent complete turns with an optional leading
// system prompt. The limit is soft: the oldest selected user turn is retained
// in full. Display pagination must not use this to trim the model's context.
func PageRecentMessages(messages []db.Message, limit int) []db.Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	prefix := 0
	if messages[0].Role == "system" {
		prefix = 1
	}
	start := max(prefix, len(messages)-max(1, limit-prefix))
	for start > prefix && messages[start].Role != "user" {
		start--
	}
	selected := make([]db.Message, 0, prefix+len(messages)-start)
	selected = append(selected, messages[:prefix]...)
	return append(selected, messages[start:]...)
}
