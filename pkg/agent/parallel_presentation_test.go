package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type groupedTool struct {
	name   string
	delay  time.Duration
	active *atomic.Int32
	peak   *atomic.Int32
}

func (g *groupedTool) Name() string { return g.name }
func (g *groupedTool) Definition() tool.Tool {
	return tool.NewFunctionTool(g.name, "Read-only probe", nil)
}
func (g *groupedTool) ExecutionMode() string { return "parallel" }
func (g *groupedTool) Execute(_ tool.AgentContext, _ string) (string, error) {
	active := g.active.Add(1)
	for {
		previous := g.peak.Load()
		if active <= previous || g.peak.CompareAndSwap(previous, active) {
			break
		}
	}
	defer g.active.Add(-1)
	time.Sleep(g.delay)
	return "result:" + g.name, nil
}

type groupedRenderer struct{}

func (groupedRenderer) ToolHeader(w io.Writer, _ style.UITheme, name, _ string) {
	fmt.Fprintf(w, "HEADER:%s\n", name)
}
func (groupedRenderer) ToolOutput(w io.Writer, _ style.UITheme, output string, _ bool, name, _ string, _ bool) {
	fmt.Fprintf(w, "OUTPUT:%s:%s\n", name, output)
}

func TestParallelToolsPresentEachResultUnderItsHeader(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ParallelToolCalls = true
	a := NewAgentAt(cfg, "", nil, t.TempDir())
	var active, peak atomic.Int32
	names := []string{"test_first", "test_second", "test_third"}
	delays := []time.Duration{100 * time.Millisecond, 30 * time.Millisecond, 10 * time.Millisecond}
	var calls []db.ToolCall
	for i, name := range names {
		a.Registry.Register(&groupedTool{name: name, delay: delays[i], active: &active, peak: &peak})
		calls = append(calls, db.ToolCall{ID: name, Type: "function", Function: db.ToolFunction{Name: name, Arguments: "{}"}})
	}
	var replayed []string
	engine := NewTurnEngine(a, EnginePolicy{})
	engine.SetBeforeToolCall(func(_ int, tc db.ToolCall) {
		replayed = append(replayed, tc.Function.Name)
	})
	msg := &db.Message{Role: "assistant", ToolCalls: calls}
	sink := &capturedHistory{}
	var output bytes.Buffer
	halted, aborted := engine.ExecuteToolCalls(context.Background(), msg, sink, NewFallbackStreamRenderer(io.Discard), nil, &output, style.GetTheme("plain"), groupedRenderer{}, nil)
	if halted || aborted {
		t.Fatalf("unexpected stop: halted=%t aborted=%t", halted, aborted)
	}
	if peak.Load() < 2 {
		t.Fatalf("tool workers did not overlap: peak=%d", peak.Load())
	}
	if !reflect.DeepEqual(replayed, names) {
		t.Fatalf("preview order: got %v, want %v", replayed, names)
	}
	text := output.String()
	position := 0
	for _, name := range names {
		header := "HEADER:" + name
		result := "OUTPUT:" + name + ":result:" + name
		h := strings.Index(text[position:], header)
		if h < 0 {
			t.Fatalf("missing header for %s: %q", name, text)
		}
		position += h + len(header)
		r := strings.Index(text[position:], result)
		if r < 0 {
			t.Fatalf("missing adjacent result for %s: %q", name, text)
		}
		between := text[position : position+r]
		if strings.Contains(between, "HEADER:") {
			t.Fatalf("a different header appeared before %s's result: %q", name, text)
		}
		position += r + len(result)
	}
	if len(sink.messages) != len(names) {
		t.Fatalf("expected %d tool messages, got %d", len(names), len(sink.messages))
	}
}
