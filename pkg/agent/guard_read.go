package agent

import (
	"context"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type readResultChecker interface {
	ReadResultCurrent(context.Context, tool.AgentContext, string, string, string) (bool, error)
}

// Revalidate only a read that would otherwise be blocked. This check never
// records source coverage; the actual read must still run and show its result.
func (e *TurnEngine) refreshReadGuard(ctx context.Context, call db.ToolCall) bool {
	if call.Function.Name != "read" || ctx.Err() != nil || e.agent == nil || e.agent.Registry == nil {
		return false
	}
	registry := e.agent.Registry
	if registry.IsDisabled("read") || !run.ToolVisible(ctx, "read") || e.policy.ToolAllowlist != nil && !tool.IsAllowedTool("read", e.policy.ToolAllowlist()) {
		return false
	}
	key := buildCallKey(call.Function.Name, call.Function.Arguments)
	prior := e.guard.outcomes[key]
	if prior.failed || prior.file == "" || prior.revision == "" {
		return false
	}
	executor, exists := registry.Lookup("read")
	checker, ok := executor.(readResultChecker)
	if !exists || !ok {
		return false
	}
	current, err := checker.ReadResultCurrent(ctx, executionContext(e, e.agent), call.Function.Arguments, prior.file, prior.revision)
	if err != nil || current {
		return false
	}
	e.guard.forgetReadResult(key)
	return true
}
