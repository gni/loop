package agent

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type parallelProbe struct {
	name   string
	active *atomic.Int32
	peak   *atomic.Int32
}

func (p *parallelProbe) Name() string { return p.name }
func (p *parallelProbe) Definition() tool.Tool {
	return tool.NewFunctionTool(p.name, "Test parallelism", nil)
}
func (p *parallelProbe) ExecutionMode() string { return "parallel" }
func (p *parallelProbe) Execute(_ tool.AgentContext, _ string) (string, error) {
	now := p.active.Add(1)
	for {
		peak := p.peak.Load()
		if now <= peak || p.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)
	p.active.Add(-1)
	return "ok", nil
}

type capturedHistory struct{ messages []db.Message }

func (s *capturedHistory) Append(m db.Message)    { s.messages = append(s.messages, m) }
func (s *capturedHistory) Messages() []db.Message { return s.messages }

func TestSequentialVersusParallelToolExecution(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprint("parallel=", parallel), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.ParallelToolCalls = parallel
			a := NewAgentAt(cfg, "", nil, t.TempDir())
			var active, peak atomic.Int32
			names := []string{"test_a", "test_b", "test_c"}
			calls := make([]db.ToolCall, 0, len(names))
			for _, name := range names {
				a.Registry.Register(&parallelProbe{name: name, active: &active, peak: &peak})
				calls = append(calls, db.ToolCall{ID: name, Type: "function", Function: db.ToolFunction{Name: name, Arguments: "{}"}})
			}
			engine := NewTurnEngine(a, EnginePolicy{})
			msg := &db.Message{Role: "assistant", ToolCalls: calls}
			sink := &capturedHistory{}
			halted, aborted := engine.ExecuteToolCalls(context.Background(), msg, sink, NewFallbackStreamRenderer(io.Discard), nil, io.Discard, style.GetTheme("plain"), NewMainRenderer(a), nil)
			if halted || aborted {
				t.Fatalf("execution unexpectedly stopped: halted=%t aborted=%t", halted, aborted)
			}
			if len(sink.messages) != len(calls) {
				t.Fatalf("want %d tool responses got %d", len(calls), len(sink.messages))
			}
			if parallel && peak.Load() < 2 {
				t.Errorf("parallel enabled but peak concurrency=%d", peak.Load())
			}
			if !parallel && peak.Load() != 1 {
				t.Errorf("sequential mode peak concurrency=%d", peak.Load())
			}
		})
	}
}
