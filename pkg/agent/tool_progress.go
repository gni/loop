package agent

import (
	"bytes"
	"io"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// toolProgressWriter keeps live shell output and a redraw in the same critical
// section. The foreground renderer can replay an unfinished line without losing
// bytes arriving from the child process during the screen rebuild.
type toolProgressWriter struct {
	mu      sync.Mutex
	writer  io.Writer
	content bytes.Buffer
}

func (w *toolProgressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.writer.Write(p)
	_, _ = w.content.Write(p[:n])
	return n, err
}

// executeToolWithProgress keeps redraw requests on the presentation goroutine
// while a tool is working. Hooks, history and result rendering remain serialized
// by the caller, and the executor still runs exactly once.
func (e *TurnEngine) executeToolWithProgress(executor tool.ToolExecutor, tc db.ToolCall, sr StreamRenderer) (tool.ToolResult, error) {
	a := e.agent
	ctx := executionContext(e, a)
	if a.OnStreamProgress == nil || e.policy.AgentName != "" {
		return tool.ExecutePreparedResult(ctx, executor, tc.Function.Arguments)
	}

	live := &toolProgressWriter{writer: a.CurrentWriter()}
	a.stateMu.Lock()
	previousWriter := a.liveToolWriter
	a.liveToolWriter = live
	a.stateMu.Unlock()
	a.CurrentStreamMu.Lock()
	previousReplay := a.CurrentStreamReplay
	// This callback runs while the foreground loop holds live.mu.
	a.CurrentStreamReplay = func(w io.Writer, collapsed bool, currentTheme style.UITheme) {
		if previousReplay != nil {
			previousReplay(w, collapsed, currentTheme)
		}
		if live.content.Len() == 0 {
			return
		}
		if collapsed && a.UI != nil {
			a.UI.RenderToolOutput(w, live.content.String(), false, true, currentTheme, tc.Function.Name, tc.Function.Arguments, false)
		} else {
			_, _ = w.Write(live.content.Bytes())
		}
	}
	a.CurrentStreamMu.Unlock()
	defer func() {
		a.CurrentStreamMu.Lock()
		a.CurrentStreamReplay = previousReplay
		a.CurrentStreamMu.Unlock()
		a.stateMu.Lock()
		a.liveToolWriter = previousWriter
		a.stateMu.Unlock()
	}()

	type result struct {
		rich tool.ToolResult
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rich, err := tool.ExecutePreparedResult(ctx, executor, tc.Function.Arguments)
		done <- result{rich, err}
	}()
	refresh := time.NewTicker(33 * time.Millisecond)
	defer refresh.Stop()
	for {
		select {
		case result := <-done:
			return result.rich, result.err
		case <-refresh.C:
			live.mu.Lock()
			a.refreshStreamPresentation(sr)
			live.mu.Unlock()
		}
	}
}
