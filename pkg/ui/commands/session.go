package commands

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func resetSessionSnapshots(a *agent.Agent) {
	if a != nil && a.Guided != nil {
		a.Guided.ClearSnapshots()
	}
}

func handleClearCommand(
	a *agent.Agent,
	messages *[]db.Message,
	currentSessionID *string,
	rlHistory term.History,
	kiReader *interceptor.KeyInterceptorReader,
	w io.Writer,
) {
	freshMessages := []db.Message{{Role: "system", Content: a.GetSystemPrompt()}}
	if !a.Ephemeral {
		if err := db.ClearSession(*currentSessionID); err != nil {
			fmt.Fprintf(w, "cannot clear session: %v\n", err)
			return
		}
		if err := a.SaveMessage(*currentSessionID, freshMessages[0]); err != nil {
			fmt.Fprintf(w, "cannot save session: %v\n", err)
			return
		}
	}
	if err := a.SetTodos(nil); err != nil {
		fmt.Fprintf(w, "cannot clear plan: %v\n", err)
		return
	}
	a.MessagesMu.Lock()
	*messages = freshMessages
	a.MessagesMu.Unlock()
	a.SyncSessionSystemPrompt(messages, "")
	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatsText = ""
	ui.GetUI().StateMu.Unlock()
	a.ClearTasks()
	resetSessionSnapshots(a)
	if ch, ok := rlHistory.(interface{ Clear() }); ok {
		ch.Clear()
	}
	if kiReader != nil {
		kiReader.ClearQueue()
	}
	if a.ClearAgentsFunc != nil {
		a.ClearAgentsFunc()
	}

	ui.GetUI().StateMu.Lock()
	ui.GetUI().State.QueuedPromptsCount = 0
	ui.GetUI().State.CompletionTokens = 0
	ui.GetUI().StateMu.Unlock()

	a.CurrentStreamMu.Lock()
	a.CurrentStreamBuffer = nil
	a.CurrentStreamMu.Unlock()

	const clearNotice = "conversation cleared and started a new one."
	if history := ui.GetUI().ChatHistory; history != nil {
		history.SetNotice(clearNotice)
		ui.MarkSessionReloaded()
		return
	}
	ui.RedrawScreenWithNotice(w, a, kiReader, terminalFromReader(kiReader), clearNotice)
}

func handleSessionCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	if a.Ephemeral && len(parts) > 1 {
		switch parts[1] {
		case "new", "clear":
			a.MessagesMu.Lock()
			*messages = nil
			a.MessagesMu.Unlock()
			*currentSessionID = ""
			a.SyncSessionSystemPrompt(messages, "")
			_ = a.SetTodos(nil)
			if ui.GetUI().ChatHistory != nil {
				ui.MarkSessionReloaded()
			}
		default:
			fmt.Fprintln(w, "this conversation is ephemeral; persistent session operations require a saved session")
		}
		return
	}
	rl := terminalFromReader(kiReader)

	resetToFreshSession := func() error {
		nextID := db.NewUUID()
		if err := db.SetLatestSessionID(nextID); err != nil {
			return fmt.Errorf("activate new session: %w", err)
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().LastStatsText = ""
		ui.GetUI().StateMu.Unlock()
		resetSessionSnapshots(a)
		*currentSessionID = nextID
		a.MessagesMu.Lock()
		*messages = nil
		a.MessagesMu.Unlock()
		a.SyncSessionSystemPrompt(messages, "")
		return nil
	}

	refreshSessionUI := func() {
		if ui.GetUI().ChatHistory != nil {
			ui.MarkSessionReloaded()
			return
		}
		syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, true)
	}

	if len(parts) <= 1 {
		fmt.Fprintf(w, "active session: %s\n", *currentSessionID)
		fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id> | clear]")
		return
	}

	switch parts[1] {
	case "list":
		sessions, err := db.GetSessions()
		if err != nil {
			fmt.Fprintf(w, "cannot list sessions: %v\n", err)
			return
		}
		if len(sessions) == 0 {
			fmt.Fprintln(w, "no past sessions found.")
			return
		}
		fmt.Fprintln(w, "past sessions:")
		for _, s := range sessions {
			activeMarker := ""
			if s.SessionID == *currentSessionID {
				activeMarker = " (active)"
			}
			previewText := s.Preview
			if len(previewText) > 40 {
				previewText = previewText[:40] + "..."
			}
			timestamp := s.Timestamp
			if len(timestamp) > 16 {
				timestamp = timestamp[:16]
			}
			fmt.Fprintf(w, "  - %s [%s] (%d messages) - %s%s\n", s.SessionID, timestamp, s.MsgCount, previewText, activeMarker)
		}

	case "new":
		if err := resetToFreshSession(); err != nil {
			fmt.Fprintf(w, "cannot create session: %v\n", err)
			return
		}
		refreshSessionUI()

	case "branch":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /session branch <new_session_id>")
			return
		}
		branchID := parts[2]
		existing, err := db.LoadMessages(branchID)
		if err != nil {
			fmt.Fprintf(w, "cannot inspect session %q: %v\n", branchID, err)
			return
		}
		if len(existing) > 0 {
			fmt.Fprintf(w, "error: session '%s' already exists.\n", branchID)
			return
		}
		if err := db.ClearSession(branchID); err != nil {
			fmt.Fprintf(w, "cannot prepare session %q: %v\n", branchID, err)
			return
		}
		for _, msg := range *messages {
			if err := db.SaveMessage(branchID, msg); err != nil {
				fmt.Fprintf(w, "cannot branch session %q: %v\n", branchID, err)
				if cleanupErr := db.ClearSession(branchID); cleanupErr != nil {
					fmt.Fprintf(w, "cannot remove incomplete branch %q: %v\n", branchID, cleanupErr)
				}
				return
			}
		}
		if err := db.SetLatestSessionID(branchID); err != nil {
			fmt.Fprintf(w, "branch %q was saved, but could not be activated: %v\n", branchID, err)
			return
		}
		*currentSessionID = branchID
		fmt.Fprintf(w, "successfully branched session into '%s'. active session is now '%s'.\n", branchID, branchID)
		ui.DrawStatusBar(os.Stderr, *theme)

	case "load":
		if len(parts) > 2 {
			selected := parts[2]
			dbHistory, err := db.LoadMessages(selected)
			if err != nil {
				fmt.Fprintf(w, "cannot load session %q: %v\n", selected, err)
				return
			}
			if len(dbHistory) == 0 {
				fmt.Fprintf(w, "error: session '%s' not found or empty.\n", selected)
				return
			}
			if err := applyLoadedSession(selected, dbHistory, currentSessionID, messages, a); err != nil {
				fmt.Fprintf(w, "cannot activate session %q: %v\n", selected, err)
				return
			}
			if ui.GetUI().ChatHistory != nil {
				ui.MarkSessionReloaded()
			} else {
				ui.RedrawScreen(w, a, kiReader, rl)
			}
			return
		}

		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		selected, startNew, err := interactive.RunSessionExplorer(*theme, input, output)
		ui.InitStatusBar(os.Stderr)
		sessionChanged := false
		if err == nil {
			if startNew {
				if newErr := resetToFreshSession(); newErr != nil {
					fmt.Fprintf(w, "cannot create session: %v\n", newErr)
				} else {
					sessionChanged = true
				}
			} else if selected != "" {
				dbHistory, loadErr := db.LoadMessages(selected)
				switch {
				case loadErr != nil:
					fmt.Fprintf(w, "cannot load session %q: %v\n", selected, loadErr)
				case len(dbHistory) == 0:
					fmt.Fprintf(w, "session %q is empty.\n", selected)
				default:
					if activateErr := applyLoadedSession(selected, dbHistory, currentSessionID, messages, a); activateErr != nil {
						fmt.Fprintf(w, "cannot activate session %q: %v\n", selected, activateErr)
					} else {
						sessionChanged = true
					}
				}
			}
		}
		if sessionChanged {
			if ui.GetUI().ChatHistory != nil {
				ui.MarkSessionReloaded()
			} else {
				ui.RedrawScreen(w, a, kiReader, rl)
			}
		} else {
			ui.RestoreAfterModal(w, a, kiReader, rl)
		}

	case "clear":
		if err := db.ClearHistory(); err != nil {
			fmt.Fprintf(w, "error clearing sessions: %v\n", err)
			return
		}
		if err := resetToFreshSession(); err != nil {
			fmt.Fprintf(w, "sessions deleted, but a new session could not be activated: %v\n", err)
			return
		}
		fmt.Fprintln(w, "all conversation sessions deleted from disk.")
		refreshSessionUI()

	default:
		fmt.Fprintf(w, "active session: %s\n", *currentSessionID)
		fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id> | clear]")
	}
}

func handleCompressCommand(
	a *agent.Agent,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	calcHistoryTokens func() (int, int, bool),
) {
	a.CompressHistory(context.Background(), messages, *currentSessionID, *theme, w)
	pTok, cTok, estimated := calcHistoryTokens()
	effLimit := a.GetEffectiveContextLimit(pTok)
	ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
	ui.DrawStatusBar(os.Stderr, *theme)
}

func applyLoadedSession(selected string, dbHistory []db.Message, currentSessionID *string, messages *[]db.Message, a *agent.Agent) error {
	if err := db.SetLatestSessionID(selected); err != nil {
		return err
	}
	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatsText = ""
	ui.GetUI().StateMu.Unlock()
	resetSessionSnapshots(a)
	*currentSessionID = selected
	a.MessagesMu.Lock()
	*messages = dbHistory
	a.MessagesMu.Unlock()
	a.SyncSessionSystemPrompt(messages, selected)
	return nil
}
