package ui

import (
	"context"

	"loop/pkg/agent"
	"loop/pkg/db"
)

func sessionDisplayLabel(a *agent.Agent, id string) (string, bool) {
	label := id
	if len(label) > 8 {
		label = label[:8]
	}
	if id == "" {
		return "unsaved", true
	}
	name, known := a.SessionNameSnapshot()
	if name != "" {
		label = name
	}
	return label, known
}

// Resolve display metadata once per session switch, not on every status redraw.
func (h *ChatHistory) setSessionContext(id string) {
	label, known := sessionDisplayLabel(h.a, id)

	state := getUI()
	state.StateMu.Lock()
	state.SessionID = id
	state.State.SessionLabel = label
	state.StateMu.Unlock()
	if known || h.a.Ephemeral {
		return
	}
	store := h.a.Store
	theme := GetConfiguredTheme(h.a.Config)
	go func() {
		var sessions []db.SessionInfo
		if contextual, ok := store.(interface {
			GetSessionsContext(context.Context) ([]db.SessionInfo, error)
		}); ok {
			sessions, _ = contextual.GetSessionsContext(h.a.Context())
		} else if store != nil {
			sessions, _ = store.GetSessions()
		} else {
			sessions, _ = db.GetSessionsContext(h.a.Context())
		}
		for _, session := range sessions {
			if session.SessionID != id || session.Name == "" {
				continue
			}
			TerminalMu.Lock()
			defer TerminalMu.Unlock()
			if h.closed.Load() {
				return
			}
			state.StateMu.Lock()
			if state.SessionID != id || state.State.SessionLabel != label {
				state.StateMu.Unlock()
				return
			}
			state.State.SessionLabel = session.Name
			state.StateMu.Unlock()
			DrawStatusBarLocked(h.reader.W, theme)
			return
		}
	}()
}
