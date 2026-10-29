package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type earlyReadProbe struct {
	name    string
	started chan<- string
	release <-chan struct{}
}

func (p *earlyReadProbe) Name() string { return p.name }
func (p *earlyReadProbe) Definition() tool.Tool {
	return tool.NewFunctionTool(p.name, "Parallel read presentation probe", nil)
}
func (p *earlyReadProbe) ExecutionMode() string { return "parallel" }
func (p *earlyReadProbe) Execute(_ tool.AgentContext, _ string) (string, error) {
	p.started <- p.name
	if p.release != nil {
		<-p.release
	}
	return "content:" + p.name, nil
}

type synchronizedTranscript struct {
	mu   sync.Mutex
	text strings.Builder
}

func (w *synchronizedTranscript) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text.Write(p)
}
func (w *synchronizedTranscript) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text.String()
}

type earlyReadRenderer struct{}

func (earlyReadRenderer) ToolHeader(w io.Writer, _ style.UITheme, name, _ string) {
	fmt.Fprintf(w, "HEADER:%s\n", name)
}
func (earlyReadRenderer) ToolOutput(w io.Writer, _ style.UITheme, output string, _ bool, _, _ string, _ bool) {
	fmt.Fprintf(w, "OUTPUT:%s\n", output)
}

func TestParallelReadRendersFirstResultBeforeLastCompletes(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ParallelToolCalls = true
	a := NewAgentAt(cfg, "", nil, t.TempDir())
	started := make(chan string, 3)
	releaseLast := make(chan struct{})
	calls := make([]db.ToolCall, 0, 3)
	for i, name := range []string{"probe_read_first", "probe_read_second", "probe_read_last"} {
		var release <-chan struct{}
		if i == 2 {
			release = releaseLast
		}
		a.Registry.Register(&earlyReadProbe{name: name, started: started, release: release})
		calls = append(calls, db.ToolCall{ID: name, Type: "function", Function: db.ToolFunction{Name: name, Arguments: "{}"}})
	}
	engine := NewTurnEngine(a, EnginePolicy{})
	out := &synchronizedTranscript{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = engine.ExecuteToolCalls(context.Background(), &db.Message{Role: "assistant", ToolCalls: calls}, &capturedHistory{}, NewFallbackStreamRenderer(io.Discard), nil, out, style.GetTheme("plain"), earlyReadRenderer{}, nil)
	}()
	// Ensure the third operation is in flight and held while the first result renders.
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(releaseLast)
			t.Fatal("parallel workers did not start")
		}
	}
	deadline := time.After(3 * time.Second)
	for !strings.Contains(out.String(), "OUTPUT:content:probe_read_first") {
		select {
		case <-deadline:
			close(releaseLast)
			t.Fatalf("first result was buffered until last worker: %q", out.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if strings.Contains(out.String(), "HEADER:probe_read_last") {
		close(releaseLast)
		t.Fatal("last call was rendered before it completed")
	}
	close(releaseLast)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("parallel execution did not finish")
	}
}
