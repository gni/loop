package repl

import (
	"os"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
)

// refreshREPLStatus updates status bar token usage and refreshes the console after a turn or cancellation.
func refreshREPLStatus(
	a *agent.Agent,
	messages []db.Message,
	allowedTools []string,
	mam *swarm.MultiAgentManager,
	kiReader *interceptor.KeyInterceptorReader,
	rl *term.Terminal,
	isCancellation bool,
) {
	activeTasks := a.CountActiveTasks()
	pTok, cTok, estimated := interceptor.CalculateActiveTokenUsage(a, messages, allowedTools, mam)
	latestTurnTokens := 0
	if !isCancellation {
		latestTurnTokens = a.GetLatestAssistantCompletionTokens(messages)
	}
	effLimit := a.GetEffectiveContextLimit(pTok)
	ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)
	if isCancellation {
		ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader, rl)
	} else {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
	}
}
