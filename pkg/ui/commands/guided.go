package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
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
	case "toggle":
		a.SetGuidedMode(!a.IsGuidedMode())
	case "on", "enable", "true", "1":
		a.SetGuidedMode(true)
	case "off", "disable", "false", "0":
		a.SetGuidedMode(false)
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
		var snaps []agent.FileSnapshot
		if a.Guided != nil {
			snaps = a.Guided.Snapshots()
		}
		fmt.Fprintf(w, "guided mode: %v (phase: %s, approved: %v, snapshots: %d)\n",
			a.IsGuidedMode(), a.Guided.Phase(), a.Guided.IsPlanApproved(), len(snaps))
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
	ui.DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, *theme)
	ui.DrawStatusBar(os.Stderr, *theme)
	if kiReader != nil && kiReader.RL != nil {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, kiReader.RL)
	}
}

func handleRollbackCommand(a *agent.Agent, theme *style.UITheme, w io.Writer) {
	if a == nil || a.Guided == nil || len(a.Guided.Snapshots()) == 0 {
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
	if a == nil || messages == nil || len(*messages) == 0 {
		fmt.Fprintln(w, "nothing to undo: conversation history is empty.")
		return
	}

	a.MessagesMu.Lock()
	lastUserIdx := -1
	for i := len(*messages) - 1; i >= 0; i-- {
		if (*messages)[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	if lastUserIdx < 0 {
		a.MessagesMu.Unlock()
		fmt.Fprintln(w, "nothing to undo: no user turn found in history.")
		return
	}

	userMsg := (*messages)[lastUserIdx]
	userPreview := strings.TrimSpace(userMsg.Content)
	if len(userPreview) > 60 {
		userPreview = userPreview[:60] + "..."
	}

	removedCount := len(*messages) - lastUserIdx
	*messages = (*messages)[:lastUserIdx]
	a.MessagesMu.Unlock()

	if a.Guided != nil && len(a.Guided.Snapshots()) > 0 {
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

	if !a.Ephemeral && currentSessionID != nil && *currentSessionID != "" {
		if err := db.RewriteSessionContext(a.Context(), *currentSessionID, *messages); err != nil {
			fmt.Fprintf(w, "warning: failed to persist session after undo: %v\n", err)
		}
	}

	a.SyncSessionSystemPrompt(messages, "")

	if history := ui.GetUI().ChatHistory; history != nil {
		ui.MarkSessionReloaded()
	} else if kiReader != nil {
		ui.RedrawScreen(w, a, kiReader, terminalFromReader(kiReader))
	}

	fmt.Fprintf(w, "undid last turn: %q (%d message(s) removed, %d remaining).\n", userPreview, removedCount, len(*messages))
}
