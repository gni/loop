package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/ui/style"
)

func (e *TurnEngine) ExecuteToolCalls(
	ctx context.Context,
	assistantMsg *db.Message,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
) (halted bool, aborted bool) {
	a := e.agent
	if a == nil || assistantMsg == nil {
		return false, false
	}
	e.batchFailure = ""
	defer func() { e.batchFailure = "" }()
	defer func() {
		if marker, ok := sr.(PendingWritePresenter); ok {
			marker.MarkPendingWriteResults("Not executed")
		}
	}()

	calls := assistantMsg.ToolCalls
	if len(calls) == 0 {
		return false, false
	}

	for i := 0; i < len(calls); {
		if e.canRunToolInParallel(calls[i]) {
			j := i + 1
			for j < len(calls) && e.canRunToolInParallel(calls[j]) {
				j++
			}
			if j-i > 1 {
				halted, aborted := e.executeToolCallsParallelRange(ctx, assistantMsg, i, j, sink, sr, loader, w, theme, render, afterCall)
				if halted || aborted {
					return halted, aborted
				}
				i = j
				continue
			}
		}

		halted, aborted := e.ExecuteToolCallAt(ctx, i, calls[i], calls[i:], assistantMsg.Content, sink, sr, loader, w, theme, render, afterCall)
		if halted || aborted {
			if aborted {
				e.RecordInterruption(sink, calls[i+1:], "aborted by user")
			}
			return halted, aborted
		}
		i++
	}
	return false, false
}

// ExecuteToolCallAt runs one completed call through the same allowlist,
// schema, approval, observation and hook checks as the batch executor. It
// keeps the original batch index for inline completion and lifecycle events.
func (e *TurnEngine) ExecuteToolCallAt(
	ctx context.Context, idx int, tc db.ToolCall, remaining []db.ToolCall,
	assistantContent string, sink HistorySink, sr StreamRenderer,
	loader EngineLoader, w io.Writer, theme style.UITheme,
	render EngineRenderer,
	afterCall func(db.ToolCall, string, error),
) (halted bool, aborted bool) {
	plan, halted, aborted := e.prepareToolCallPlan(ctx, idx, tc, remaining, assistantContent, false, false, sink, sr, loader, w, theme, render)
	if halted || aborted {
		return halted, aborted
	}
	tc = plan.call
	if plan.skipped {
		e.emitRejectedToolCall(idx, tc, plan.output, !plan.approvalRendered, sr.DidStreamToolBody(idx), sink, sr, w, theme, render, plan.dependencySkipped)
		return false, false
	}
	if !plan.guardHit {
		hookOut, hookErr := e.checkBeforeHook(tc)
		if ctx.Err() != nil {
			e.RecordInterruption(sink, remaining, "cancelled before execution")
			return true, false
		}
		if hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
			plan.rich.Evidence = &evidence.Record{Kind: "tool", Target: tc.Function.Name, Outcome: evidence.NotExecuted}
		} else {
			if loader != nil {
				if tc.Function.Name == "bash" {
					loader.PauseDots()
				} else {
					loader.ShowDots()
				}
			}
			plan.rich, plan.toolErr = e.executeToolWithProgress(plan.executor, tc, sr)
			plan.output = plan.rich.Output
			if ctx.Err() != nil {
				e.recordInterruptedCall(sink, tc, "cancelled during execution", plan.rich.Evidence)
				if len(remaining) > 1 {
					e.RecordInterruption(sink, remaining[1:], "cancelled before execution")
				}
				return true, false
			}
			plan.output, plan.toolErr = e.applyAfterHook(tc, plan.output, plan.toolErr)
		}
	}
	e.finalizeToolCall(idx, tc, plan.output, plan.toolErr, plan.approvalRendered, true, sink, sr, w, theme, render, afterCall, plan.rich)
	if e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
		if len(remaining) > 1 {
			e.RecordInterruption(sink, remaining[1:], "loop protection halted execution")
		}
		e.emitHaltNotice(sink, w, theme)
		return true, false
	}
	return false, false
}

func (e *TurnEngine) executeToolCallsParallelRange(
	ctx context.Context,
	assistantMsg *db.Message,
	startIndex, endIndex int,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
) (halted bool, aborted bool) {
	a := e.agent
	calls := assistantMsg.ToolCalls[startIndex:endIndex]
	plans := make([]callPlan, len(calls))

	for batchIndex, tc := range calls {
		callIndex := startIndex + batchIndex
		plan, halted, aborted := e.prepareToolCallPlan(ctx, callIndex, tc, assistantMsg.ToolCalls[callIndex:], assistantMsg.Content, true, true, sink, sr, loader, w, theme, render)
		if halted || aborted {
			reason := "cancelled before execution"
			if aborted {
				reason = "aborted by user"
				e.RecordInterruption(sink, assistantMsg.ToolCalls[callIndex+1:], reason)
			}
			e.RecordInterruption(sink, assistantMsg.ToolCalls[startIndex:callIndex], reason)
			return halted, aborted
		}
		plans[batchIndex] = plan
	}

	workers := a.GetLimits().ParallelToolWorkers
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	// Tool execution remains parallel, but the renderer consumes each result
	// as soon as the next call in model order completes. We never wait for
	// the entire worker pool before presenting the first completed read.
	ready := make(chan int, len(plans))

	for i := range plans {
		plan := &plans[i]
		if plan.skipped || plan.guardHit {
			ready <- i
			continue
		}
		if ctx.Err() != nil {
			plan.toolErr = ctx.Err()
			plan.output = "cancelled before execution"
			ready <- i
			continue
		}

		if hookOut, hookErr := e.checkBeforeHook(plan.call); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
			plan.rich.Evidence = &evidence.Record{Kind: "tool", Target: plan.call.Function.Name, Outcome: evidence.NotExecuted}
			ready <- i
			continue
		}

		if strings.HasPrefix(plan.call.Function.Name, "subagent__") {
			plan.started = true
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), plan.executor, plan.call.Function.Arguments)
			plan.rich = rich
			plan.output, plan.toolErr = e.applyAfterHook(plan.call, rich.Output, err)
			ready <- i
			continue
		}

		wg.Add(1)
		go func(i int, p *callPlan) {
			defer wg.Done()
			defer func() { ready <- i }()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				p.toolErr = ctx.Err()
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled before execution")
				return
			}

			if ctx.Err() != nil {
				p.toolErr = ctx.Err()
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}

			p.started = true
			a.SetLiveBodyStreamed(false)
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), p.executor, p.call.Function.Arguments)
			p.rich = rich
			if ctx.Err() != nil {
				p.toolErr = ctx.Err()
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}
			p.output, p.toolErr = e.applyAfterHook(p.call, rich.Output, err)
		}(i, plan)
	}

	go func() {
		wg.Wait()
		close(ready)
	}()

	shouldHalt := false
	completed := make([]bool, len(plans))
	next := 0
	refresh := time.NewTicker(33 * time.Millisecond)
	defer refresh.Stop()
	for ready != nil {
		var idx int
		select {
		case completedIndex, ok := <-ready:
			if !ok {
				ready = nil
				continue
			}
			idx = completedIndex
		case <-refresh.C:
			a.refreshStreamPresentation(sr)
			continue
		}
		completed[idx] = true
		for next < len(plans) && completed[next] {
			if ctx.Err() != nil {
				// Drain worker completions before returning; do not render results
				// that would suggest a cancelled tool completed normally.
				break
			}
			plan := &plans[next]
			callIndex := startIndex + next
			if callIndex > 0 {
				if counter, ok := w.(*NewlineCounterWriter); ok {
					counter.EnsureActivityGap()
				} else {
					fmt.Fprint(w, strings.Repeat("\n", style.ActivityGapLines+1))
				}
			}
			e.ensureToolHeaderRendered(callIndex, plan.call, assistantMsg.Content, false, sr, w, theme, render)
			if plan.skipped {
				e.emitRejectedToolCall(callIndex, plan.call, plan.output, !plan.approvalRendered, false, sink, sr, w, theme, render, plan.dependencySkipped)
			} else {
				e.finalizeToolCall(callIndex, plan.call, plan.output, plan.toolErr, plan.approvalRendered, false, sink, sr, w, theme, render, afterCall, plan.rich)
			}
			if plan.guardHit && e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
				shouldHalt = true
			}
			next++
		}
	}
	if ctx.Err() != nil {
		// All workers have completed, so their started flags are stable.
		// Calls cancelled while queued must not claim an unknown side effect.
		for i := next; i < len(plans); i++ {
			reason := "cancelled before execution"
			if plans[i].started {
				reason = "cancelled during execution"
			}
			e.recordInterruptedCall(sink, assistantMsg.ToolCalls[startIndex+i], reason, plans[i].rich.Evidence)
		}
		e.RecordInterruption(sink, assistantMsg.ToolCalls[endIndex:], "cancelled before execution")
		return true, false
	}

	if shouldHalt {
		e.RecordInterruption(sink, assistantMsg.ToolCalls[endIndex:], "loop protection halted execution")
		e.emitHaltNotice(sink, w, theme)
		return true, false
	}
	return false, false
}
