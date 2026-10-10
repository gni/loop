package agent

import (
	"context"
	"io"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// executeToolCalls is now a thin wrapper over the shared TurnEngine. The pipeline itself
// (approval, guard, hooks, execution, defense formatting, pruning/spill, rendering,
// pairing, circuit breaker) lives once in engine.go and is shared with the subagent loop.
func (a *Agent) executeToolCalls(
	ctx context.Context,
	assistantMsg *db.Message,
	messages *[]db.Message,
	sr StreamRenderer,
	loader *turnLoader,
	ncw io.Writer,
	theme style.UITheme,
	guard *TurnExecutionGuard,
	sessionID string,
	iter int,
	consecutiveGuardRejections *int,
) (halted bool, aborted bool) {
	engine := newEngineFromGuard(a, guard, sessionID, iter)
	if consecutiveGuardRejections != nil {
		engine.consecutiveGuardRejections = *consecutiveGuardRejections
	}
	halted, aborted = engine.ExecuteToolCalls(ctx, assistantMsg, NewAgentSink(a, messages, sessionID), sr, loader, ncw, theme, NewMainRenderer(a), nil)
	if consecutiveGuardRejections != nil {
		*consecutiveGuardRejections = engine.consecutiveGuardRejections
	}
	return halted, aborted
}

// newEngineFromGuard reuses an existing per-turn guard instance so the guard state is not
// reset between steps of the same turn.
func newEngineFromGuard(a *Agent, guard *TurnExecutionGuard, sessionID string, iter int) *TurnEngine {
	e := NewTurnEngine(a, EnginePolicy{SessionKey: sessionID})
	if guard != nil {
		e.guard = guard
	}
	e.awareness.Turn = iter
	return e
}

// recordToolCallInterruption kept for API stability; delegates to the engine.
func (a *Agent) recordToolCallInterruption(messages *[]db.Message, calls []db.ToolCall, reason, sessionID string) {
	e := NewTurnEngine(a, EnginePolicy{SessionKey: sessionID})
	e.RecordInterruption(NewAgentSink(a, messages, sessionID), calls, reason)
}
