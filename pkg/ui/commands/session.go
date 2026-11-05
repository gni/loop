package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

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
			a.SetActiveSessionID("")
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
		a.SetActiveSessionID(nextID)
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
		fmt.Fprintln(w, "usage: /session [list | new | load [id] | tree | branch <id> | rename <id> | clear]")
		return
	}

	switch parts[1] {
	case "tree":
		handleTreeCommand(a, parts, currentSessionID, theme, w)
		return

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
			previewText := truncateString(s.Preview, 40)
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
			fmt.Fprintln(w, "usage: /session branch <new_session_id|entry_id>")
			return
		}
		target := parts[2]
		if currentSessionID != nil && *currentSessionID != "" {
			if entries, err := db.Entries(*currentSessionID); err == nil && len(entries) > 0 {
				if matchedID, err := resolveEntryID(entries, target); err == nil {
					handleBranchCommand(a, []string{"/branch", matchedID}, messages, currentSessionID, theme, w, kiReader)
					return
				}
			}
		}
		branchID := target
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
		a.SetActiveSessionID(branchID)
		fmt.Fprintf(w, "successfully branched session into '%s'. active session is now '%s'.\n", branchID, branchID)
		ui.DrawStatusBar(os.Stderr, *theme)

	case "rename":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /session rename <new_name>")
			return
		}
		if currentSessionID == nil || *currentSessionID == "" {
			fmt.Fprintln(w, "error: no active session to rename.")
			return
		}
		oldID := *currentSessionID
		newName := strings.TrimSpace(parts[2])
		if newName == "" {
			fmt.Fprintln(w, "error: target session name cannot be empty.")
			return
		}
		if err := db.RenameSession(oldID, newName); err != nil {
			fmt.Fprintf(w, "cannot rename session: %v\n", err)
			return
		}
		*currentSessionID = newName
		a.SetActiveSessionID(newName)
		fmt.Fprintf(w, "session renamed from '%s' to '%s'. active session is now '%s'.\n", oldID, newName, newName)
		if ui.GetUI().ChatHistory != nil {
			ui.MarkSessionReloaded()
		}
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
		fmt.Fprintln(w, "usage: /session [list | new | load [id] | tree | branch <id> | rename <id> | clear]")
	}
}

func handleCompressCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	calcHistoryTokens func() (int, int, bool),
) {
	sessionID := ""
	if currentSessionID != nil {
		sessionID = *currentSessionID
	}
	var focusInstructions string
	if len(parts) > 1 {
		focusInstructions = strings.TrimSpace(strings.Join(parts[1:], " "))
	}
	ctx := a.Lifetime
	if ctx == nil {
		ctx = a.Context()
	}
	if err := a.CompressHistory(ctx, messages, sessionID, *theme, w, focusInstructions); err != nil {
		fmt.Fprintf(w, "cannot compact session: %v\n", err)
		return
	}
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
	a.SetActiveSessionID(selected)
	a.MessagesMu.Lock()
	*messages = dbHistory
	a.MessagesMu.Unlock()
	a.SyncSessionSystemPrompt(messages, selected)
	return nil
}
