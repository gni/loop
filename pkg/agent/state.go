package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"loop/pkg/config"
	"loop/pkg/resources"
	"loop/pkg/ui/style"
)

func (a *Agent) SetActiveSessionID(sessionID string) {
	a.stateMu.Lock()
	changed := a.activeSessionID != sessionID
	if changed {
		a.sessionGeneration++
		a.sessionName, a.sessionNameKnown, a.sessionNameDirty = "", false, false
	}
	a.activeSessionID = sessionID
	a.stateMu.Unlock()
	if changed {
		path := ""
		if !a.Ephemeral && sessionID != "" {
			path = filepath.Join(resources.AgentDirectory(a.ConfigPath), "recovery", config.WorkspaceKey(a.WorkspaceRoot), config.WorkspaceKey(sessionID)+".json")
		}
		if err := a.ensureGuided().bindRecovery(a.Context(), path, a.WorkspaceRoot); err != nil {
			a.recordPersistenceError(err)
		}
	}
}

func (a *Agent) ActiveSessionID() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.activeSessionID
}

func (a *Agent) SetPendingSystemEvent(event string) {
	if a == nil || event == "" {
		return
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.pendingSystemEvent == "" {
		a.pendingSystemEvent = event
	} else {
		a.pendingSystemEvent += "; " + event
	}
}

func (a *Agent) TakePendingSystemEvent() string {
	if a == nil {
		return ""
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	event := a.pendingSystemEvent
	a.pendingSystemEvent = ""
	return event
}

func (a *Agent) GetExecutionEnv() map[string]string {
	if a == nil {
		return nil
	}
	env := map[string]string{
		"LOOP_WORKSPACE": a.WorkspaceRoot,
	}
	a.stateMu.Lock()
	sessionID := a.activeSessionID
	a.stateMu.Unlock()
	if sessionID != "" {
		env["LOOP_SESSION_ID"] = sessionID
	}
	a.LLMProviderMu.RLock()
	if a.Config != nil {
		if model := a.Config.Model; model != "" {
			env["LOOP_MODEL"] = model
		}
		if provider := a.Config.ActiveProvider; provider != "" {
			env["LOOP_PROVIDER"] = provider
		}
	}
	a.LLMProviderMu.RUnlock()
	return env
}

func (a *Agent) Context() context.Context {
	a.stateMu.Lock()
	ctx := a.currentContext
	a.stateMu.Unlock()
	if ctx != nil {
		return ctx
	}
	if a.Lifetime != nil {
		return a.Lifetime
	}
	return context.Background()
}

// withLifetime lets both the caller and session shutdown stop an operation.
func (a *Agent) withLifetime(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.Lifetime == nil {
		return ctx, func() {}
	}
	linked, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(a.Lifetime, cancel)
	if a.Lifetime.Err() != nil {
		cancel()
	}
	return linked, func() { stop(); cancel() }
}

func (a *Agent) CurrentWriter() io.Writer {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.currentWriter
}

func (a *Agent) CurrentTheme() style.UITheme {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.currentTheme
}

func (a *Agent) LastGenerationDuration() time.Duration {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.lastGenerationDuration
}

func (a *Agent) recordDroppedChunk() {
	if a == nil {
		return
	}
	a.stateMu.Lock()
	a.droppedChunks++
	a.lastTurnDropped++
	a.stateMu.Unlock()
}

func (a *Agent) DroppedChunks() int64 {
	if a == nil {
		return 0
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.droppedChunks
}

func (a *Agent) TurnDroppedChunks() int64 {
	if a == nil {
		return 0
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.lastTurnDropped
}

func (a *Agent) SetTurnState(w io.Writer, ctx context.Context, theme style.UITheme) {
	a.stateMu.Lock()
	a.currentWriter = w
	a.currentContext = ctx
	a.currentTheme = theme
	a.lastTurnDropped = 0
	a.stateMu.Unlock()
}

func (a *Agent) ClearTurnState() {
	a.stateMu.Lock()
	a.currentWriter = nil
	a.currentContext = nil
	a.currentTheme = style.UITheme{}
	a.stateMu.Unlock()
}

func (a *Agent) SetCurrentWriter(w io.Writer) {
	a.stateMu.Lock()
	a.currentWriter = w
	a.stateMu.Unlock()
}

func (a *Agent) SetLastGenerationDuration(d time.Duration) {
	a.stateMu.Lock()
	a.lastGenerationDuration = d
	a.stateMu.Unlock()
}

func (a *Agent) GetLiveWriter() io.Writer {
	a.stateMu.Lock()
	w := a.liveToolWriter
	a.stateMu.Unlock()
	if w != nil {
		return w
	}
	return a.CurrentWriter()
}

func (a *Agent) GetChatWriter() io.Writer {
	if a == nil {
		return os.Stderr
	}
	if a.UI != nil {
		if wh, ok := a.UI.(interface{ GetPPWriter() io.Writer }); ok {
			if w := wh.GetPPWriter(); w != nil {
				return w
			}
		}
	}
	if w := a.CurrentWriter(); w != nil {
		return w
	}
	return os.Stderr
}

func (a *Agent) SetLiveBodyStreamed(streamed bool) {
	a.stateMu.Lock()
	a.liveBodyStreamed = streamed
	a.stateMu.Unlock()
}

func (a *Agent) DidStreamLiveBody() bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.liveBodyStreamed
}
