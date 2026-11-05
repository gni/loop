package agent

import (
	"context"
	"io"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type dummyRenderer struct{}

func (d *dummyRenderer) ToolHeader(w io.Writer, theme style.UITheme, toolName, args string) {}
func (d *dummyRenderer) ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool) {
}

func TestEngine_StagedDAGToolExecution(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ParallelToolCalls = true
	a := NewAgentAt(cfg, "", nil, t.TempDir())

	engine := NewTurnEngine(a, EnginePolicy{})
	var msgs []db.Message
	sink := NewAgentSink(a, &msgs, "")
	theme := style.GetTheme("dark")
	sr := a.NewTurnStreamRenderer(io.Discard, theme, "loop", "")
	renderer := &dummyRenderer{}

	// Create 3 tool calls: read, read, todo (non-parallel)
	assistantMsg := &db.Message{
		Role: "assistant",
		ToolCalls: []db.ToolCall{
			{
				ID:       "call_1",
				Function: db.ToolFunction{Name: "read", Arguments: `{"path": "foo.txt"}`},
			},
			{
				ID:       "call_2",
				Function: db.ToolFunction{Name: "read", Arguments: `{"path": "bar.txt"}`},
			},
			{
				ID:       "call_3",
				Function: db.ToolFunction{Name: "todo", Arguments: `{"action": "list"}`},
			},
		},
	}

	var executedOrder []string
	afterCall := func(tc db.ToolCall, output string, toolErr error) {
		executedOrder = append(executedOrder, tc.Function.Name)
	}

	halted, aborted := engine.ExecuteToolCalls(
		context.Background(),
		assistantMsg,
		sink,
		sr,
		nil,
		io.Discard,
		theme,
		renderer,
		afterCall,
	)

	t.Logf("halted=%v, aborted=%v, executedOrder=%v", halted, aborted, executedOrder)

	if halted || aborted {
		t.Fatalf("expected clean execution, got halted=%v, aborted=%v", halted, aborted)
	}

	if len(executedOrder) != 3 {
		t.Fatalf("expected 3 executed calls, got %d: %v", len(executedOrder), executedOrder)
	}

	// First two were reads (parallel stage), third was todo (serial stage)
	if executedOrder[0] != "read" || executedOrder[1] != "read" || executedOrder[2] != "todo" {
		t.Errorf("unexpected execution order: %v", executedOrder)
	}
}
