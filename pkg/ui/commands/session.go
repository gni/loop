package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

// Drop transient work before entering another conversation. Failed loads leave
// the current conversation intact because callers run this only after validation.
func resetConversationState(a *agent.Agent, reader *interceptor.KeyInterceptorReader) {
	state := ui.GetUI()
	state.StateMu.Lock()
	if reader == nil {
		reader, _ = state.ActiveInputReader.(*interceptor.KeyInterceptorReader)
	}
	state.LastStatsText = ""
	state.State.QueuedPromptsCount = 0
	state.State.CompletionTokens = 0
	if reader != nil {
		reader.ResetTypeAheadLocked()
	}
	state.StateMu.Unlock()
	if reader != nil {
		reader.ClearQueue()
	}
	a.ClearTasks()
	a.CurrentStreamMu.Lock()
	a.CurrentStreamBuffer = nil
	a.CurrentStreamReplay = nil
	a.CurrentStreamMu.Unlock()
}

func clearSessionWork(a *agent.Agent, w io.Writer) {
	_ = a.SetTodos(nil)
	if a.ClearAgentsFunc != nil {
		if err := a.ClearAgentsFunc(); err != nil {
			fmt.Fprintf(w, "could not clear saved subagents: %v\n", err)
		}
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
		if err := db.ClearSessionContext(a.Context(), *currentSessionID); err != nil {
			fmt.Fprintf(w, "cannot clear session: %v\n", err)
			return
		}
	}
	resetConversationState(a, kiReader)
	clearSessionWork(a, w)
	a.MessagesMu.Lock()
	*messages = freshMessages
	a.MessagesMu.Unlock()
	a.SyncSessionSystemPrompt(messages, "")
	if ch, ok := rlHistory.(interface{ Clear() }); ok {
		ch.Clear()
	}

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
			resetConversationState(a, kiReader)
			clearSessionWork(a, w)
			a.MessagesMu.Lock()
			*messages = nil
			a.MessagesMu.Unlock()
			*currentSessionID = ""
			a.SetActiveSessionID("")
			a.SyncSessionSystemPrompt(messages, "")
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
		if err := db.SetLatestSessionIDContext(a.Context(), nextID); err != nil {
			return fmt.Errorf("activate new session: %w", err)
		}
		if a.SyncSessionState != nil {
			if err := a.SyncSessionState(nil, a.ToolSelection, nextID, false); err != nil {
				return err
			}
		} else {
			_ = a.SetTodos(nil)
		}
		resetConversationState(a, kiReader)
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
		fmt.Fprintln(w, "usage: /session [list | new | load [id] | tree | branch <id> | rename <name> | clear]")
		return
	}

	switch parts[1] {
	case "tree":
		handleTreeCommand(a, parts, currentSessionID, theme, w)
		return

	case "list":
		sessions, err := db.GetSessionsContext(a.Context())
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
			if entries, err := db.EntriesContext(a.Context(), *currentSessionID); err == nil && len(entries) > 0 {
				if matchedID, err := resolveEntryID(entries, target); err == nil {
					handleBranchCommand(a, []string{"/branch", matchedID}, messages, currentSessionID, theme, w, kiReader)
					return
				}
			}
		}
		branchID := target
		existing, err := db.LoadMessagesContext(a.Context(), branchID)
		if err != nil {
			fmt.Fprintf(w, "cannot inspect session %q: %v\n", branchID, err)
			return
		}
		if len(existing) > 0 {
			fmt.Fprintf(w, "error: session '%s' already exists.\n", branchID)
			return
		}
		hasContent := false
		for _, msg := range *messages {
			if msg.HasConversationContent() {
				hasContent = true
				break
			}
		}
		if !hasContent {
			fmt.Fprintln(w, "there is no conversation to branch yet.")
			return
		}
		if err := db.RewriteSessionContext(a.Context(), branchID, *messages); err != nil {
			fmt.Fprintf(w, "cannot branch session %q: %v\n", branchID, err)
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
		newName := strings.TrimSpace(strings.Join(parts[2:], " "))
		if newName == "" {
			fmt.Fprintln(w, "error: target session name cannot be empty.")
			return
		}
		if err := a.SetSessionName(commandContext(a, kiReader), newName); err != nil {
			fmt.Fprintf(w, "cannot rename session: %v\n", err)
			return
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().State.SessionLabel = newName
		ui.GetUI().StateMu.Unlock()
		fmt.Fprintf(w, "session named %q.\n", newName)

	case "load":
		if len(parts) > 2 {
			selected := parts[2]
			if err := loadSavedSession(selected, currentSessionID, messages, a); err != nil {
				fmt.Fprintf(w, "cannot load session %q: %v\n", selected, err)
				return
			}
			if ui.GetUI().ChatHistory != nil {
				ui.MarkSessionReloaded()
			} else {
				ui.RedrawScreen(w, a, kiReader, rl)
			}
			return
		}

		input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		selected, startNew, err := interactive.RunSessionExplorerContext(commandContext(a, kiReader), *theme, input, output)
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
				if loadErr := loadSavedSession(selected, currentSessionID, messages, a); loadErr != nil {
					fmt.Fprintf(w, "cannot load session %q: %v\n", selected, loadErr)
				} else {
					sessionChanged = true
				}
			}
		}
		if err != nil && !errors.Is(err, interactive.ErrSelectionCancelled) {
			fmt.Fprintf(w, "cannot open session picker: %v\n", err)
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
		if err := db.ClearHistoryContext(a.Context()); err != nil {
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
		fmt.Fprintln(w, "usage: /session [list | new | load [id] | tree | branch <id> | rename <name> | clear]")
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
	if a.SyncSessionState != nil {
		if err := a.SyncSessionState(*messages, a.ToolSelection, sessionID, false); err != nil {
			fmt.Fprintf(w, "compacted history is active, but could not save session state: %v\n", err)
		}
	}
	pTok, cTok, estimated := calcHistoryTokens()
	effLimit := a.GetEffectiveContextLimit(pTok)
	ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
	ui.DrawStatusBar(os.Stderr, *theme)
}

func applyLoadedSession(selected string, dbHistory []db.Message, currentSessionID *string, messages *[]db.Message, a *agent.Agent) error {
	store, err := db.DefaultStore()
	if err != nil {
		return err
	}
	previousLatest, err := store.GetLatestSessionIDContext(a.Context())
	if err != nil {
		return err
	}
	if err := store.SetLatestSessionIDContext(a.Context(), selected); err != nil {
		return err
	}
	if a.SyncSessionState != nil {
		if err := a.SyncSessionState(dbHistory, nil, selected, false); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(a.Context()), 2*time.Second)
			defer cancel()
			return errors.Join(err, store.SetLatestSessionIDContext(cleanupCtx, previousLatest))
		}
		if restored, ok := a.SessionMessagesSnapshot(); ok {
			dbHistory = restored
		}
	}
	resetConversationState(a, nil)
	*currentSessionID = selected
	a.SetActiveSessionID(selected)
	a.MessagesMu.Lock()
	*messages = dbHistory
	a.MessagesMu.Unlock()
	a.SyncSessionSystemPrompt(messages, selected)
	return nil
}

// Avoid passing a typed nil reader through an io.Reader interface.
func sessionInputReader(reader *interceptor.KeyInterceptorReader) io.Reader {
	if reader == nil {
		return nil
	}
	return reader
}

func loadSavedSession(selected string, currentSessionID *string, messages *[]db.Message, a *agent.Agent) error {
	history, err := a.LoadSessionMessages(selected)
	if err != nil {
		return err
	}
	for _, msg := range history {
		if msg.HasConversationContent() {
			return applyLoadedSession(selected, history, currentSessionID, messages, a)
		}
	}
	return fmt.Errorf("session not found or empty")
}
