package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	bashtool "loop/pkg/agent/tool/bash"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type callPlan struct {
	call              db.ToolCall
	executor          tool.ToolExecutor
	output            string
	toolErr           error
	skipped           bool
	dependencySkipped bool
	duplicateOf       string
	started           bool
	guardHit          bool
	approvalRendered  bool
	rich              tool.ToolResult
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
	// Tool calls have no dependency graph. After a failed prerequisite, defer
	// remaining actions until the model has inspected the actual result.
	if e.batchFailure != "" && !e.isBatchReadOnlyCall(tc) {
		plan.skipped, plan.dependencySkipped = true, true
		plan.output = fmt.Sprintf("Not executed: earlier tool %s failed or reported syntax errors. Review its result, then issue any still-needed actions in a new turn. Read-only inspections may continue.", e.batchFailure)
		return plan, false, false
	}

	guardErr := e.checkGuardPreExecution(ctx, tc)
	if ctx.Err() != nil {
		e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
		return plan, true, false
	}
	if guardErr != nil {
		plan.toolErr = guardErr
		plan.output = guardErr.Error()
		plan.guardHit = true
		return plan, false, false
	}

	if e.policy.ToolAllowlist != nil && !tool.IsAllowedTool(tc.Function.Name, e.policy.ToolAllowlist()) {
		plan.toolErr = fmt.Errorf("tool '%s' is not allowed for this agent", tc.Function.Name)
		plan.output = plan.toolErr.Error()
		plan.skipped = true
		return plan, false, false
	}
	if e.agent.Registry.IsDisabled(tc.Function.Name) {
		if tool.NormalizeName(tc.Function.Name) == "ask_user" {
			plan.toolErr = fmt.Errorf("tool %q is disabled; ask the user directly in plain text instead", tc.Function.Name)
		} else {
			plan.toolErr = fmt.Errorf("tool %q is disabled", tc.Function.Name)
		}
		plan.output = plan.toolErr.Error()
		plan.skipped = true
		return plan, false, false
	}
	if !run.ToolVisible(ctx, tc.Function.Name) {
		_, exists := e.agent.Registry.Lookup(tc.Function.Name)
		if !exists && e.agent.Registry.UnknownToolHook != nil {
			_, exists = e.agent.Registry.UnknownToolHook(tc.Function.Name)
		}
		if !exists {
			plan.toolErr = fmt.Errorf("unknown tool: %s", tc.Function.Name)
			plan.output = plan.toolErr.Error()
			plan.skipped = true
			return plan, false, false
		}
		if e.agent.Registry.IsHidden(tc.Function.Name) {
			plan.skipped = true
			if run.ToolVisible(ctx, "search_tools") {
				plan.output = fmt.Sprintf("Tool %q was not exposed for this step. Discover it with search_tools before calling it.", tc.Function.Name)
			} else {
				plan.output = fmt.Sprintf("Tool %q was not exposed for this step.", tc.Function.Name)
			}
			return plan, false, false
		}
		// Enabled, allowed, non-hidden tools whose schema was omitted only to
		// save prompt tokens remain callable directly; expose them for later steps.
		run.NoteDiscoveredTool(ctx, tc.Function.Name)
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

	gateErr := e.verifyGuidedGate(ctx, sr, loader, tc)
	if ctx.Err() != nil {
		e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
		return plan, true, false
	}
	if gateErr != nil {
		plan.toolErr = gateErr
		plan.output = gateErr.Error()
		plan.skipped = true
		if strings.Contains(plan.output, "aborted by user") {
			e.emitRejectedToolCall(idx, tc, plan.output, true, bodyStreamed, sink, sr, w, theme, render, false)
			return plan, false, true
		}
		return plan, false, false
	}

	approvalRendered, approvalErr := e.requestApproval(ctx, sr, loader, w, theme, idx, tc)
	plan.approvalRendered = approvalRendered
	if ctx.Err() != nil {
		e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
		return plan, true, false
	}
	if errors.Is(approvalErr, errToolApprovalRejected) {
		rejOutput, shouldAbort := e.promptRejectionFeedback(tc.Function.Name)
		if ctx.Err() != nil {
			e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
			return plan, true, false
		}
		if shouldAbort {
			e.emitRejectedToolCall(idx, tc, rejOutput, !approvalRendered, bodyStreamed, sink, sr, w, theme, render, false)
			return plan, false, true
		}
		plan.toolErr = approvalErr
		plan.output = rejOutput
		plan.skipped = true
		return plan, false, false
	}
	if approvalErr != nil {
		plan.toolErr, plan.output, plan.skipped = approvalErr, approvalErr.Error(), true
	}

	return plan, false, false
}

func (e *TurnEngine) isBatchReadOnlyCall(tc db.ToolCall) bool {
	if e.agent != nil && e.agent.Registry != nil && e.agent.Registry.Policy(tc.Function.Name, tc.Function.Arguments).ReadOnly {
		return true
	}
	return tool.NormalizeName(tc.Function.Name) == "bash" && bashtool.IsReadOnlyArguments(tc.Function.Arguments)
}
