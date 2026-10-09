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

func handleClearCommand(
	a *agent.Agent,
	messages *[]db.Message,
	currentSessionID *string,
	rlHistory term.History,
	kiReader *interceptor.KeyInterceptorReader,
	w io.Writer,
) {
	ui.GetUI().LastStatsText = ""
	if a != nil {
		a.ClearTasks()
	}
	*messages = []db.Message{
		{Role: "system", Content: a.GetSystemPrompt()},
	}
	_ = db.ClearSession(*currentSessionID)
	_ = db.SaveMessage(*currentSessionID, (*messages)[0])
	if ch, ok := rlHistory.(interface{ Clear() }); ok {
		ch.Clear()
	}
	if kiReader != nil {
		kiReader.ClearQueue()
		ui.GetUI().StateMu.Lock()
		ui.GetUI().State.QueuedPromptsCount = 0
		ui.GetUI().StateMu.Unlock()
	}

	if a.ClearAgentsFunc != nil {
		a.ClearAgentsFunc()
	}

	ui.GetUI().StateMu.Lock()
	ui.GetUI().State.CompletionTokens = 0
	ui.GetUI().StateMu.Unlock()

	a.CurrentStreamMu.Lock()
	a.CurrentStreamBuffer = nil
	a.CurrentStreamMu.Unlock()

	var rl *term.Terminal
	if kiReader != nil {
		rl = kiReader.RL
	}
	ui.RedrawScreenWithNotice(w, a, kiReader, rl, "conversation cleared and started a new one.")
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
	if len(parts) > 1 {
		sub := parts[1]
		switch sub {
		case "list":
			sessions, err := db.GetSessions()
			if err != nil || len(sessions) == 0 {
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
				fmt.Fprintf(w, "  - %s [%s] (%d messages) - %s%s\n", s.SessionID, s.Timestamp[:16], s.MsgCount, previewText, activeMarker)
			}
		case "new":
			ui.GetUI().LastStatsText = ""
			*currentSessionID = db.NewUUID()
			_ = db.SetLatestSessionID(*currentSessionID)
			*messages = []db.Message{
				{Role: "system", Content: a.GetSystemPrompt()},
			}
			pTok, cTok, estimated := calcHistoryTokens()
			effLimit := a.GetEffectiveContextLimit(pTok)
			ui.GetUI().StateMu.Lock()
			ui.GetUI().LastStatusBarText = ""
			ui.GetUI().StateMu.Unlock()
			ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
			var rl *term.Terminal
			if kiReader != nil {
				rl = kiReader.RL
			}
			ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
			ui.DrawStatusBar(os.Stderr, *theme)
		case "branch":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /session branch <new_session_id>")
				return
			}
			branchID := parts[2]
			existing, err := db.LoadMessages(branchID)
			if err == nil && len(existing) > 0 {
				fmt.Fprintf(w, "error: session '%s' already exists.\n", branchID)
				return
			}

			_ = db.ClearSession(branchID)
			for _, msg := range *messages {
				_ = db.SaveMessage(branchID, msg)
			}
			*currentSessionID = branchID
			_ = db.SetLatestSessionID(*currentSessionID)
			fmt.Fprintf(w, "successfully branched session into '%s'. active session is now '%s'.\n", branchID, branchID)
			ui.DrawStatusBar(os.Stderr, *theme)
		case "load":
			if len(parts) > 2 {
				selected := parts[2]
				dbHistory, err := db.LoadMessages(selected)
				if err == nil && len(dbHistory) > 0 {
					applyLoadedSession(selected, dbHistory, currentSessionID, messages, a)
					if kiReader != nil {
						ui.RedrawScreen(w, a, kiReader, kiReader.RL)
					} else {
						ui.RedrawScreen(w, a, nil, nil)
					}
				} else {
					fmt.Fprintf(w, "error: session '%s' not found or empty.\n", selected)
				}
				return
			}

			input, output, cleanup := interactive.GetInteractiveIO(kiReader)
			defer cleanup()

			selected, startNew, err := interactive.RunSessionExplorer(*theme, input, output)
			ui.InitStatusBar(os.Stderr)
			if err == nil {
				ui.GetUI().LastStatsText = ""
				if startNew {
					*currentSessionID = db.NewUUID()
					_ = db.SetLatestSessionID(*currentSessionID)
					*messages = []db.Message{
						{Role: "system", Content: a.GetSystemPrompt()},
					}
				} else if selected != "" {
					dbHistory, loadErr := db.LoadMessages(selected)
					if loadErr == nil && len(dbHistory) > 0 {
						applyLoadedSession(selected, dbHistory, currentSessionID, messages, a)
					}
				}
			}

			if kiReader != nil {
				ui.RedrawScreen(w, a, kiReader, kiReader.RL)
			} else {
				ui.RedrawScreen(w, a, nil, nil)
			}
		case "clear":
			err := db.ClearHistory()
			if err != nil {
				fmt.Fprintf(w, "error clearing sessions: %v\n", err)
				return
			}
			ui.GetUI().LastStatsText = ""
			*currentSessionID = db.NewUUID()
			_ = db.SetLatestSessionID(*currentSessionID)
			*messages = []db.Message{
				{Role: "system", Content: a.GetSystemPrompt()},
			}
			fmt.Fprintln(w, "all conversation sessions deleted from disk.")
			pTok, cTok, estimated := calcHistoryTokens()
			effLimit := a.GetEffectiveContextLimit(pTok)
			ui.GetUI().StateMu.Lock()
			ui.GetUI().LastStatusBarText = ""
			ui.GetUI().StateMu.Unlock()
			ui.UpdateStatus(a.Config.Model, pTok, cTok, 0, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
			var rl *term.Terminal
			if kiReader != nil {
				rl = kiReader.RL
			}
			ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
			ui.DrawStatusBar(os.Stderr, *theme)
		default:
			fmt.Fprintf(w, "active session: %s\n", *currentSessionID)
			fmt.Fprintln(w, "usage: /session [list | new | load | branch <new_session_id> | clear]")
		}
	} else {
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

func applyLoadedSession(selected string, dbHistory []db.Message, currentSessionID *string, messages *[]db.Message, a *agent.Agent) {
	ui.GetUI().LastStatsText = ""
	*currentSessionID = selected
	_ = db.SetLatestSessionID(*currentSessionID)
	*messages = dbHistory
	if len(*messages) > 0 && (*messages)[0].Role == "system" {
		currentSysPrompt := a.GetSystemPrompt()
		if (*messages)[0].Content != currentSysPrompt {
			(*messages)[0].Content = currentSysPrompt
			_ = db.RewriteSession(selected, *messages)
		}
	}
}
