package repl

import (
	"os"

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
	live := a.LiveSettings()
	ui.UpdateStatus(live.Model, pTok, cTok, latestTurnTokens, effLimit, false, tps, activeTasks, live.ShowTokens, estimated)
	if isCancellation {
		ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader)
	} else {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader)
	}
}
