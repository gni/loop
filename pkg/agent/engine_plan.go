package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type callPlan struct {
	call             db.ToolCall
	executor         tool.ToolExecutor
	output           string
	toolErr          error
	skipped          bool
	guardHit         bool
	approvalRendered bool
	rich             tool.ToolResult
}

func (e *TurnEngine) prepareToolCallPlan(
	ctx context.Context,
	idx int,
	tc db.ToolCall,
	remainingCalls []db.ToolCall,
	assistantContent string,
	isParallel bool,
	deferPresentation bool,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
) (plan callPlan, halted bool, aborted bool) {
	plan = callPlan{call: tc}
	e.agent.emit(Event{Type: "tool_execution_start", SessionID: e.policy.SessionKey, ToolCall: &tc})
	if ctx.Err() != nil {
		e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
		return plan, true, false
	}
	if idx > 0 && !deferPresentation {
		// A streamed write with a reserved result row needs no extra spacing
		// during execution. Its completion is shown beside its own preview.
		inlineResult := false
		if tc.Function.Name == "write" || tc.Function.Name == "edit" {
			if pending, ok := sr.(interface{ HasPendingWriteResult(int) bool }); ok {
				inlineResult = pending.HasPendingWriteResult(idx)
			}
		}
		if !inlineResult {
			if counter, ok := w.(*NewlineCounterWriter); ok {
				counter.EnsureActivityGap()
			} else {
				fmt.Fprint(w, strings.Repeat("\n", style.ActivityGapLines+1))
			}
		}
	}
	if !isParallel && e.agent != nil {
		e.agent.SetLiveBodyStreamed(false)
	}

	// In a concurrent batch, prepare and execute without printing headings.
	// Render each heading directly before its result, in model order, after
	// the workers complete. This keeps reads, searches, and plugin results
	// paired with the call that produced them.
	if !deferPresentation {
		e.ensureToolHeaderRendered(idx, tc, assistantContent, isParallel, sr, w, theme, render)
	}
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.Start(idx)
	}

	bodyStreamed := !isParallel && sr.DidStreamToolBody(idx)

	if e.policy.ToolAllowlist != nil && !tool.IsAllowedTool(tc.Function.Name, e.policy.ToolAllowlist()) {
		plan.toolErr = fmt.Errorf("tool '%s' is not allowed for this agent", tc.Function.Name)
		plan.output = plan.toolErr.Error()
		plan.skipped = true
		return plan, false, false
	}

	executor, prepared, err := e.agent.Registry.Prepare(tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		plan.toolErr = err
		plan.output = err.Error()
		plan.skipped = true
		return plan, false, false
	}
	tc.Function.Arguments = prepared
	plan.call = tc
	plan.executor = executor

	if gateErr := e.verifyGuidedGate(sr, loader, tc.Function.Name); gateErr != nil {
		plan.toolErr = gateErr
		plan.output = gateErr.Error()
		plan.skipped = true
		if strings.Contains(plan.output, "aborted by user") {
			e.emitRejectedToolCall(idx, tc, plan.output, true, bodyStreamed, sink, sr, w, theme, render)
			return plan, false, true
		}
		return plan, false, false
	}

	approved, approvalRendered := e.requestApproval(sr, loader, w, theme, idx, tc)
	plan.approvalRendered = approvalRendered
	if !approved {
		rejOutput, shouldAbort := e.promptRejectionFeedback(tc.Function.Name)
		if shouldAbort {
			e.emitRejectedToolCall(idx, tc, rejOutput, !approvalRendered, bodyStreamed, sink, sr, w, theme, render)
			return plan, false, true
		}
		plan.toolErr = fmt.Errorf("rejected by user")
		plan.output = rejOutput
		plan.skipped = true
		return plan, false, false
	}

	if guardErr := e.checkGuardPreExecution(tc); guardErr != nil {
		plan.toolErr = guardErr
		plan.output = guardErr.Error()
		plan.guardHit = true
	}

	return plan, false, false
}
