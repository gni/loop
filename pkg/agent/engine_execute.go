package agent

import (
	"context"
	"fmt"
	"io"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
	"strings"
	"sync"
	"time"
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
	defer func() {
		if marker, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
			marker.MarkPendingWriteResults("Not executed")
		}
	}()

	if e.shouldRunToolCallsParallel(assistantMsg.ToolCalls) {
		return e.executeToolCallsParallel(ctx, assistantMsg, sink, sr, loader, w, theme, render, afterCall)
	}

	for idx, tc := range assistantMsg.ToolCalls {
		halted, aborted := e.ExecuteToolCallAt(ctx, idx, tc, assistantMsg.ToolCalls[idx:], assistantMsg.Content, sink, sr, loader, w, theme, render, afterCall)
		if halted || aborted {
			if aborted {
				e.RecordInterruption(sink, assistantMsg.ToolCalls[idx+1:], "aborted by user")
			}
			return halted, aborted
		}
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
	tc = plan.tc
	if plan.skipped {
		e.emitRejectedToolCall(idx, tc, plan.output, !plan.approvalRendered, sr.DidStreamToolBody(idx), sink, sr, w, theme, render)
		return false, false
	}
	if !plan.guardHit {
		if hookOut, hookErr := e.checkBeforeHook(tc); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
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
				e.RecordInterruption(sink, remaining, "cancelled during execution")
				return true, false
			}
			plan.output, plan.toolErr = e.applyAfterHook(tc, plan.output, plan.toolErr)
		}
	}
	e.finalizeToolCall(idx, tc, plan.output, plan.toolErr, plan.approvalRendered, true, sink, sr, w, theme, render, afterCall, plan.rich)
	if e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
		e.emitHaltNotice(sink, w, theme)
		return true, false
	}
	return false, false
}

func (e *TurnEngine) executeToolCallsParallel(
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
	plans := make([]callPlan, len(assistantMsg.ToolCalls))

	for idx, tc := range assistantMsg.ToolCalls {
		plan, halted, aborted := e.prepareToolCallPlan(ctx, idx, tc, assistantMsg.ToolCalls[idx:], assistantMsg.Content, true, true, sink, sr, loader, w, theme, render)
		if halted || aborted {
			reason := "cancelled before execution"
			if aborted {
				reason = "aborted by user"
				e.RecordInterruption(sink, assistantMsg.ToolCalls[idx+1:], reason)
			}
			e.RecordInterruption(sink, assistantMsg.ToolCalls[:idx], reason)
			return halted, aborted
		}
		plans[idx] = plan
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

		if hookOut, hookErr := e.checkBeforeHook(plan.tc); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
			ready <- i
			continue
		}

		if strings.HasPrefix(plan.tc.Function.Name, "subagent__") {
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), plan.executor, plan.tc.Function.Arguments)
			plan.rich = rich
			plan.output, plan.toolErr = e.applyAfterHook(plan.tc, rich.Output, err)
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
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}

			a.SetLiveBodyStreamed(false)
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), p.executor, p.tc.Function.Arguments)
			p.rich = rich
			if ctx.Err() != nil {
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}
			p.output, p.toolErr = e.applyAfterHook(p.tc, rich.Output, err)
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
		case next, ok := <-ready:
			if !ok {
				ready = nil
				continue
			}
			idx = next
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
			if next > 0 {
				if counter, ok := w.(*NewlineCounterWriter); ok {
					counter.EnsureActivityGap()
				} else {
					fmt.Fprint(w, strings.Repeat("\n", style.ActivityGapLines+1))
				}
			}
			e.ensureToolHeaderRendered(next, plan.tc, assistantMsg.Content, false, sr, w, theme, render)
			if plan.skipped {
				e.emitRejectedToolCall(next, plan.tc, plan.output, !plan.approvalRendered, false, sink, sr, w, theme, render)
			} else {
				e.finalizeToolCall(next, plan.tc, plan.output, plan.toolErr, plan.approvalRendered, false, sink, sr, w, theme, render, afterCall, plan.rich)
			}
			if plan.guardHit && e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
				shouldHalt = true
			}
			next++
		}
	}
	if ctx.Err() != nil {
		e.RecordInterruption(sink, assistantMsg.ToolCalls[next:], "cancelled during execution")
		return true, false
	}

	if shouldHalt {
		e.emitHaltNotice(sink, w, theme)
		return true, false
	}
	return false, false
}
