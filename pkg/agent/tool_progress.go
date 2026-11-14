package agent

import (
	"fmt"
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
	content toolProgressBuffer
}

func (w *toolProgressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.writer.Write(p)
	w.content.append(p[:n])
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
	defer live.content.close()
	if tc.Function.Name == "bash" {
		if presenter, ok := sr.(interface {
			NewShellOutputWriter(io.Writer, string) io.Writer
		}); ok {
			live.writer = presenter.NewShellOutputWriter(live.writer, tc.Function.Arguments)
		}
	}
	a.stateMu.Lock()
	previousWriter := a.liveToolWriter
	a.liveToolWriter = live
	a.stateMu.Unlock()
	a.CurrentStreamMu.Lock()
	previousReplay := a.CurrentStreamReplay
	// Foreground redraws hold live.mu. Each snapshot also owns its reader so
	// asynchronous or already queued replays cannot race with writes or cleanup.
	a.CurrentStreamReplay = func(w io.Writer, collapsed bool, currentTheme style.UITheme) {
		if previousReplay != nil {
			previousReplay(w, collapsed, currentTheme)
		}
		snapshot := live.content.snapshot()
		defer snapshot.Close()
		if snapshot.size == 0 {
			return
		}
		if snapshot.err != nil {
			fmt.Fprintln(w, "[Live output cache unavailable; retaining replay in memory.]")
		}
		if presenter, ok := sr.(interface {
			RenderShellOutputNotice(io.Writer, int, style.UITheme)
		}); collapsed && tc.Function.Name == "bash" && ok {
			presenter.RenderShellOutputNotice(w, snapshot.lines, currentTheme)
		} else if collapsed && a.UI != nil {
			data, err := io.ReadAll(snapshot.Reader)
			if err != nil {
				fmt.Fprintf(w, "[Live output replay failed: %v]\n", err)
			}
			a.UI.RenderToolOutput(w, string(data), false, true, currentTheme, tc.Function.Name, tc.Function.Arguments, false)
		} else if presenter, ok := sr.(interface {
			NewShellOutputWriter(io.Writer, string) io.Writer
		}); ok && tc.Function.Name == "bash" {
			if _, err := io.Copy(presenter.NewShellOutputWriter(w, tc.Function.Arguments), snapshot.Reader); err != nil {
				fmt.Fprintf(w, "[Live output replay failed: %v]\n", err)
			}
		} else {
			if _, err := io.Copy(w, snapshot.Reader); err != nil {
				fmt.Fprintf(w, "[Live output replay failed: %v]\n", err)
			}
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
