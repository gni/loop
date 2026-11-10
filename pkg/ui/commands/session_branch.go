package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/session"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleBranchCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
) {
	if currentSessionID == nil || *currentSessionID == "" || (a != nil && a.Ephemeral) {
		fmt.Fprintln(w, "active session is ephemeral or unset; branching requires a saved session.")
		return
	}

	if len(parts) < 2 {
		fmt.Fprintln(w, "usage: /branch <entry_id>")
		fmt.Fprintln(w, "run /tree to view entry IDs along the conversation DAG.")
		return
	}

	targetArg := strings.TrimSpace(parts[1])
	ctx := commandContext(a, kiReader)
	entries, err := db.EntriesContext(ctx, *currentSessionID)
	if err != nil {
		fmt.Fprintf(w, "cannot inspect session entries: %v\n", err)
		return
	}

	targetID, err := resolveEntryID(entries, targetArg)
	if err != nil {
		fmt.Fprintf(w, "cannot resolve entry ID: %v\n", err)
		return
	}

	leafPath := session.LeafPath(entries, "")
	if len(leafPath) > 0 && leafPath[len(leafPath)-1].ID == targetID {
		fmt.Fprintf(w, "already at entry %s.\n", shortEntryID(targetID))
		return
	}

	var projected []db.Message
	if a != nil && a.SelectSessionBranch != nil {
		projected, err = a.SelectSessionBranch(ctx, targetID)
	} else {
		projected = db.ProjectMessages(session.LeafPath(entries, targetID))
		err = db.BranchContext(ctx, *currentSessionID, targetID)
	}
	if err != nil {
		fmt.Fprintf(w, "cannot branch session: %v\n", err)
		return
	}

	if a != nil {
		a.MessagesMu.Lock()
	}
	*messages = projected
	if a != nil {
		a.MessagesMu.Unlock()
	}

	if a != nil && a.SelectSessionBranch == nil {
		a.ResetSessionApproval()
		a.SyncSessionSystemPrompt(messages, "")
	}

	if history := ui.GetUI().ChatHistory; history != nil {
		ui.MarkSessionReloaded()
	} else if kiReader != nil {
		ui.RedrawScreen(w, a, kiReader, terminalFromReader(kiReader))
	}

	fmt.Fprintf(w, "switched active branch to entry %s (%d message(s) in context).\n", shortEntryID(targetID), len(*messages))
	ui.DrawStatusBar(os.Stderr, *theme)
}

func handleForkCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	currentSessionID *string,
	theme *style.UITheme,
	w io.Writer,
) {
	if currentSessionID == nil || *currentSessionID == "" {
		fmt.Fprintln(w, "error: no active session to fork.")
		return
	}
	parentID := *currentSessionID
	forkEntryID := ""
	if len(parts) > 1 {
		forkEntryID = parts[1]
	}
	newID, err := db.ForkSessionContext(a.Context(), parentID, forkEntryID)
	if err != nil {
		fmt.Fprintf(w, "cannot fork session %s: %v\n", parentID, err)
		return
	}
	if err := loadSavedSession(newID, currentSessionID, messages, a); err != nil {
		fmt.Fprintf(w, "cannot activate forked session %s: %v\n", newID, err)
		return
	}
	fmt.Fprintf(w, "successfully forked session from '%s'. active session is now '%s'.\n", parentID, newID)
	if ui.GetUI().ChatHistory != nil {
		ui.MarkSessionReloaded()
	}
	ui.DrawStatusBar(os.Stderr, *theme)
}
