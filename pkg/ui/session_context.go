package ui

import "loop/pkg/db"

// Resolve display metadata once per session switch, not on every status redraw.
func (h *ChatHistory) setSessionContext(id string) {
	label := id
	if len(label) > 8 {
		label = label[:8]
	}
	if id == "" {
		label = "unsaved"
	}

	state := getUI()
	state.StateMu.Lock()
	state.SessionID = id
	state.State.SessionLabel = label
	state.StateMu.Unlock()
	if id == "" || h.a.Ephemeral {
		return
	}
	store := h.a.Store
	theme := GetConfiguredTheme(h.a.Config)
	go func() {
		var sessions []db.SessionInfo
		if store != nil {
			sessions, _ = store.GetSessions()
		} else {
			sessions, _ = db.GetSessions()
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
