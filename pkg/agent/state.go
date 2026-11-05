package agent

import (
	"context"
	"io"
	"os"
	"time"

	"loop/pkg/ui/style"
)

func (a *Agent) SetActiveSessionID(sessionID string) {
	a.stateMu.Lock()
	a.activeSessionID = sessionID
	a.stateMu.Unlock()
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
	cfg := a.Config
	a.stateMu.Unlock()
	if sessionID != "" {
		env["LOOP_SESSION_ID"] = sessionID
	}
	if cfg != nil {
		if model := cfg.Model; model != "" {
			env["LOOP_MODEL"] = model
		}
		if provider := cfg.ActiveProvider; provider != "" {
			env["LOOP_PROVIDER"] = provider
		}
	}
	return env
}

func (a *Agent) Context() context.Context {
	a.stateMu.Lock()
	ctx := a.currentContext
	a.stateMu.Unlock()
	if ctx != nil {
		return ctx
	}
	return context.Background()
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
