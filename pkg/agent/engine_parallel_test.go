package agent

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

type noopRenderer struct{}

func (r *noopRenderer) Write(string)                       {}
func (r *noopRenderer) WriteReasoning(string)              {}
func (r *noopRenderer) Flush()                             {}
func (r *noopRenderer) HasOutput() bool                    { return false }
func (r *noopRenderer) StartToolCall(string, int)          {}
func (r *noopRenderer) WriteToolCall(string)               {}
func (r *noopRenderer) GetToolTitleLineNumber(int) int     { return -1 }
func (r *noopRenderer) DidStreamToolBody(int) bool         { return false }
func (r *noopRenderer) CompleteToolCall(int, string, string, bool) {}
func (r *noopRenderer) GetReasoningDuration() float64      { return 0 }
func (r *noopRenderer) SetPrompt(string)                   {}

func (r *noopRenderer) ToolHeader(w io.Writer, theme style.UITheme, toolName, args string) {}
func (r *noopRenderer) ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool) {
}

type noopLoader struct{}

func (l *noopLoader) Pause()    {}
func (l *noopLoader) Resume()   {}
func (l *noopLoader) ShowDots() {}
func (l *noopLoader) Feed()     {}
func (l *noopLoader) PauseDots() {}

// concurrencyTool records the peak number of calls that overlapped, so the test
// can prove the pipeline actually runs calls concurrently and that the worker
// pool is bounded by the resolved limit.
type concurrencyTool struct {
	mu   sync.Mutex
	active int
	peak int
	wait time.Duration
}

func (c *concurrencyTool) Name() string { return "concurrent" }

func (c *concurrencyTool) PromptSnippet() string { return "concurrent test tool" }

func (c *concurrencyTool) PromptGuidelines() []string { return nil }

func (c *concurrencyTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool("concurrent", "concurrent test tool", map[string]domaintool.SchemaProp{
		"arg": domaintool.StringProp("argument"),
	}, "arg")
}

func (c *concurrencyTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.peak {
		c.peak = c.active
	}
	c.mu.Unlock()

	time.Sleep(c.wait)

	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return "ok " + arguments, nil
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func itoaParallel(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestExecuteToolCallsRunsConcurrentlyWithinBoundedPool(t *testing.T) {
	toolInstance := &concurrencyTool{wait: 60 * time.Millisecond}

	a := &Agent{
		WorkspaceRoot: t.TempDir(),
		Config: &config.Config{
			AutoApprove:        true,
			ParallelToolCalls:  true,
			RepeatGuardLimit:   10,
			MaxReasoningSteps:  30,
			MaxToolOutputBytes: 8192,
		},
		Registry: tool.NewToolRegistry(),
	}
	a.Registry.Register(toolInstance)

	engine := NewTurnEngine(a, EnginePolicy{SessionKey: "test"})
	sink := &noopSink{}

	calls := make([]db.ToolCall, 6)
	for i := range calls {
		calls[i] = db.ToolCall{ID: "call_" + itoaParallel(i), Function: db.ToolFunction{Name: "concurrent", Arguments: `{"arg":"` + itoaParallel(i) + `"}`}}
	}
	assistantMsg := &db.Message{Role: "assistant", ToolCalls: calls}

	halted, aborted := engine.ExecuteToolCalls(
		context.Background(), assistantMsg, sink,
		&noopRenderer{}, &noopLoader{},
		discardWriter{}, style.UITheme{}, &noopRenderer{}, nil,
	)
	if halted || aborted {
		t.Fatalf("unexpected halt/abort: %v %v", halted, aborted)
	}

	if len(sink.msgs) != len(calls) {
		t.Fatalf("expected %d tool messages, got %d", len(calls), len(sink.msgs))
	}

	// History order must match the order the model emitted, so tool_call_id
	// pairing stays intact even though execution overlapped.
	for i, m := range sink.msgs {
		if m.Role != "tool" || m.ToolCallID != calls[i].ID {
			t.Fatalf("message %d is out of model order: %+v", i, m)
		}
	}

	// Concurrency happened, but never past the bounded pool.
	if toolInstance.peak < 2 {
		t.Fatalf("expected concurrent execution, peak overlap was %d", toolInstance.peak)
	}
	if toolInstance.peak > limits.ParallelToolWorkers() {
		t.Fatalf("worker pool exceeded resolved limit: peak=%d limit=%d", toolInstance.peak, limits.ParallelToolWorkers())
	}

	// A single call must not spawn a pool at all.
	toolInstance.peak = 0
	sink2 := &noopSink{}
	one := &db.Message{Role: "assistant", ToolCalls: calls[:1]}
	if _, _ = engine.ExecuteToolCalls(context.Background(), one, sink2, &noopRenderer{}, &noopLoader{}, discardWriter{}, style.UITheme{}, &noopRenderer{}, nil); len(sink2.msgs) != 1 {
		t.Fatalf("single call path broken")
	}
	if toolInstance.peak > 1 {
		t.Fatalf("single call must not overlap")
	}
}

func TestParallelExecutionRespectsConfigOffSwitch(t *testing.T) {
	toolInstance := &concurrencyTool{wait: 40 * time.Millisecond}
	a := &Agent{
		WorkspaceRoot: t.TempDir(),
		Config:        &config.Config{AutoApprove: true, ParallelToolCalls: false, MaxReasoningSteps: 30},
		Registry:      tool.NewToolRegistry(),
	}
	a.Registry.Register(toolInstance)
	engine := NewTurnEngine(a, EnginePolicy{SessionKey: "test"})
	sink := &noopSink{}

	msg := &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{
		{ID: "c1", Function: db.ToolFunction{Name: "concurrent", Arguments: `{"arg":"1"}`}},
		{ID: "c2", Function: db.ToolFunction{Name: "concurrent", Arguments: `{"arg":"2"}`}},
	}}
	engine.ExecuteToolCalls(context.Background(), msg, sink, &noopRenderer{}, &noopLoader{}, discardWriter{}, style.UITheme{}, &noopRenderer{}, nil)

	if len(sink.msgs) != 1 {
		t.Fatalf("parallel disabled must trim to the first call, got %d", len(sink.msgs))
	}
	if toolInstance.peak > 1 {
		t.Fatalf("parallel disabled must not overlap")
	}
}
