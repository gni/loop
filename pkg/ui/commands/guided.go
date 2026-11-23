package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleGuidedCommand(
	a *agent.Agent,
	parts []string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
) {
	if a == nil {
		return
	}

	sub := "toggle"
	if len(parts) > 1 {
		sub = strings.ToLower(strings.TrimSpace(parts[1]))
	}

	switch sub {
	case "toggle", "on", "enable", "true", "1", "off", "disable", "false", "0":
		if err := a.UpdateLiveSettings(func(next *agent.LiveSettings) {
			switch sub {
			case "toggle":
				next.GuidedMode = !next.GuidedMode
			case "on", "enable", "true", "1":
				next.GuidedMode = true
			default:
				next.GuidedMode = false
			}
		}); err != nil {
			fmt.Fprintln(w, err)
			return
		}
	case "approve":
		todos := a.GetTodos()
		if len(todos) == 0 {
			fmt.Fprintln(w, "no active todo plan to approve.")
			return
		}
		a.Guided.ApprovePlan(todos)
		fmt.Fprintf(w, "approved plan (%d step(s)).\n", len(todos))
		return
	case "plan", "status":
		fmt.Fprintf(w, "guided mode: %v (phase: %s, approved: %v, snapshots: %d)\n",
			a.IsGuidedMode(), a.Guided.Phase(), a.Guided.IsPlanApproved(), a.Guided.SnapshotCount())
		for i, item := range a.GetTodos() {
			fmt.Fprintf(w, "  %d. [%s] %s\n", i+1, item.Status, item.Task)
		}
		return
	default:
		fmt.Fprintln(w, "usage: /guided [on | off | status | approve]")
		return
	}

	if a.ConfigPath != "" && a.Config != nil {
		saveConfigUpdate(a, w)
	}
	stateStr := "disabled"
	if a.IsGuidedMode() {
		stateStr = "enabled"
	}
	fmt.Fprintf(w, "guided mode %s.\n", stateStr)
	live := a.LiveSettings()
	ui.DrawStaticPromptSeparator(os.Stderr, live.ShowThinking, live.ReasoningEffort, *theme)
	ui.DrawStatusBar(os.Stderr, *theme)
	if kiReader != nil {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader)
	}
}

func handleRollbackCommand(a *agent.Agent, theme *style.UITheme, w io.Writer) {
	if a == nil || a.Guided.SnapshotCount() == 0 {
		fmt.Fprintln(w, "no modified files to roll back.")
		return
	}
	restored, err := a.RollbackChanges()
	if err != nil {
		fmt.Fprintf(w, "rollback error: %v\n", err)
	}
	for _, line := range restored {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

func handleUndoCommand(
	a *agent.Agent,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
) {
	if a == nil || messages == nil {
		fmt.Fprintln(w, "nothing to undo: conversation history is empty.")
		return
	}

	a.MessagesMu.RLock()
	snapshot := message.CloneAll(*messages)
	a.MessagesMu.RUnlock()
	lastUserIdx := -1
	for i := len(snapshot) - 1; i >= 0; i-- {
		if snapshot[i].IsUserInput() {
			lastUserIdx = i
			break
		}
	}

	if lastUserIdx < 0 {
		fmt.Fprintln(w, "nothing to undo: no user turn found in history.")
		return
	}

	userMsg := snapshot[lastUserIdx]
	userPreview := strings.TrimSpace(userMsg.Content)
	if runes := []rune(userPreview); len(runes) > 60 {
		userPreview = string(runes[:60]) + "..."
	}

	remaining := snapshot[:lastUserIdx:lastUserIdx]
	id := ""
	if currentSessionID != nil {
		id = *currentSessionID
	}
	if err := a.RewriteSession(a.Context(), id, remaining); err != nil {
		fmt.Fprintf(w, "could not undo turn: %v\n", err)
		return
	}
	removedCount := len(snapshot) - lastUserIdx
	a.MessagesMu.Lock()
	*messages = remaining
	a.MessagesMu.Unlock()

	if a.Guided != nil && a.Guided.SnapshotCount() > 0 {
		restored, err := a.RollbackChanges()
		if err != nil {
			fmt.Fprintf(w, "warning: file rollback encountered error: %v\n", err)
		} else if len(restored) > 0 {
			fmt.Fprintf(w, "rolled back %d file(s):\n", len(restored))
			for _, line := range restored {
				fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}

	a.SyncSessionSystemPrompt(messages, "")
	if a.SyncSessionState != nil {
		if err := a.SyncSessionState(*messages, a.ToolSelection, id, false); err != nil {
			fmt.Fprintf(w, "turn removed, but could not sync runtime state: %v\n", err)
		}
	}

	if history := ui.GetUI().GetChatHistory(); history != nil {
		ui.MarkSessionReloaded()
	} else if kiReader != nil {
		ui.RedrawScreen(w, a, kiReader)
	}

	fmt.Fprintf(w, "undid last turn: %q (%d message(s) removed, %d remaining).\n", userPreview, removedCount, len(*messages))
}
