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
	var tps float64
	if !isCancellation {
		latestTurnTokens = a.GetLatestAssistantCompletionTokens(messages)
		if dur := a.LastGenerationDuration(); dur > 0 && latestTurnTokens > 0 {
			tps = float64(latestTurnTokens) / dur.Seconds()
		}
	}
	effLimit := a.GetEffectiveContextLimit(pTok)
	ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, tps, activeTasks, a.Config.ShowTokens, estimated)
	if isCancellation {
		ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader, rl)
	} else {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
	}
}