package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/session"
)

type RunStatus string

const (
	RunCompleted    RunStatus = "completed"
	RunFailed       RunStatus = "failed"
	RunCancelled    RunStatus = "cancelled"
	RunBlocked      RunStatus = "blocked"
	RunLimitReached RunStatus = "limit_reached"
)

type RunResult struct {
	Status   RunStatus    `json:"status"`
	Text     string       `json:"text,omitempty"`
	Turns    int          `json:"turns"`
	Messages []db.Message `json:"messages,omitempty"`
	Error    string       `json:"error,omitempty"`
}

type Event struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	SessionID string    `json:"session_id,omitempty"`
	Time      time.Time `json:"time"`
	Message  *db.Message  `json:"message,omitempty"`
	Delta    string       `json:"delta,omitempty"`
	Kind     string       `json:"kind,omitempty"`
	ToolCall *db.ToolCall `json:"tool_call,omitempty"`
	Output   string       `json:"output,omitempty"`
	IsError  bool         `json:"is_error,omitempty"`
	Result   *RunResult   `json:"result,omitempty"`
	Data     any          `json:"data,omitempty"`
}

func (a *Agent) emit(event Event) {
	event.Version = 1
	event.Time = time.Now().UTC()
	if a.EventSink != nil {
		if err := a.EventSink(event); err != nil {
			a.recordPersistenceError(fmt.Errorf("event output: %w", err))
		}
	}
}

func (a *Agent) recordPersistenceError(err error) {
	if err == nil {
		return
	}
	a.stateMu.Lock()
	a.persistenceErr = errors.Join(a.persistenceErr, err)
	a.stateMu.Unlock()
}

func (a *Agent) persistenceError() error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.persistenceErr
}

func (a *Agent) SaveMessage(id string, message db.Message) error {
	if id == "" {
		return nil
	}
	if a.Store != nil {
		return a.Store.SaveMessage(id, message)
	}
	return db.SaveMessage(id, message)
}

func (a *Agent) AppendEntry(id string, record session.Record) error {
	if id == "" {
		return nil
	}
	if a.Store != nil {
		store, ok := a.Store.(interface {
			AppendEntry(string, session.Record) error
		})
		if !ok {
			return fmt.Errorf("session storage does not support durable context entries")
		}
		return store.AppendEntry(id, record)
	}
	return db.AppendEntry(id, record)
}

func (a *Agent) HasMessages(id string) bool {
	if a.Store != nil {
		return a.Store.HasMessages(id)
	}
	return db.HasMessages(id)
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
	if a.HttpClient != nil {
		a.HttpClient.CloseIdleConnections()
	}
	return ctx.Err()
}

func (a *Agent) PersistenceError() error { return a.persistenceError() }

func (a *Agent) resolveCredential(ctx context.Context) (string, error) {
	if a.Credentials == nil {
		return config.ProviderConfig{ApiKey: a.Config.ApiKey}.ResolvedApiKey(), nil
	}
	profile, exists := a.Config.Providers[a.Config.ActiveProvider]
	if !exists {
		profile = config.ProviderConfig{Endpoint: a.Config.Endpoint, ApiKey: a.Config.ApiKey}
	}
	profile.Endpoint=a.Config.Endpoint
 if strings.HasPrefix(a.Config.ApiKey, "env:") || strings.HasPrefix(a.Config.ApiKey, "$") {
		profile.ApiKey = a.Config.ApiKey
	}
	key, err := a.Credentials.Resolve(ctx, a.Config.ActiveProvider, profile)
	if key == "" && err == nil {
		key = config.ProviderConfig{ApiKey: a.Config.ApiKey}.ResolvedApiKey()
	}
	return key, err
}

func (a *Agent) LoadSessionMessages(id string) ([]db.Message, error) {
	if id == "" || a.Ephemeral {
		return nil, nil
	}
	if a.Store != nil {
		return a.Store.LoadMessages(id)
	}
	return db.LoadMessages(id)
}
