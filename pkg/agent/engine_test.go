package agent

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// fixedIntervalProvider emits tool calls until a cap, then a plain answer.
type fixedIntervalProvider struct {
	calls atomic.Int32
	cap   int
}

func (p *fixedIntervalProvider) CheckThinkingSupport(context.Context) bool { return false }

func (p *fixedIntervalProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	n := p.calls.Add(1)
	if int(n) > p.cap {
		return &db.Message{Role: "assistant", Content: "done"}, nil
	}
	return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{
		{ID: "call_" + db.NewUUID(), Function: db.ToolFunction{Name: "grep", Arguments: `{"pattern":"x"}`}},
	}}, nil
}

func TestTurnAwarenessRecapInjection(t *testing.T) {
	tmpDir := t.TempDir()

	a := &Agent{
		WorkspaceRoot: tmpDir,
		Config: &config.Config{
			ContextWindowLimit:   128000,
			CompressionThreshold: 0.8,
			MaxReasoningSteps:    12,
			AutoApprove:          true,
			RecapInterval:        3,
		},
		LLMProvider: &fixedIntervalProvider{cap: 11},
		Registry:    tool.NewToolRegistry(),
	}
	a.Registry.Register(tool.NewGrepTool())

	messages := []db.Message{{Role: "system", Content: "system"}}
	var buf bytes.Buffer
	a.RunAgentLoop(context.Background(), &buf, &messages, "search", nil, style.UITheme{}, true, "")

	recaps := 0
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, "[recap agent] turn") {
			recaps++
			if !strings.Contains(m.Content, "tokens") || !strings.Contains(m.Content, "effective window") {
				t.Fatalf("recap must report token usage, got: %s", m.Content)
			}
		}
	}
	if recaps == 0 {
		t.Fatalf("expected recap messages every 3 turns, got none; messages: %d", len(messages))
	}

	engine := NewTurnEngine(a, EnginePolicy{})
	if engine.Awareness().RecapInterval != 3 {
		t.Fatalf("expected config-driven recap interval 3, got %d", engine.Awareness().RecapInterval)
	}
	for i := 1; i <= 6; i++ {
		engine.BeginTurn()
		want := i%3 == 0
		if engine.Awareness().ShouldRecap() != want {
			t.Fatalf("turn %d: ShouldRecap=%v, want %v", i, engine.Awareness().ShouldRecap(), want)
		}
	}
}

func TestRecapDisabled(t *testing.T) {
	a := &Agent{Config: &config.Config{RecapInterval: -1, MaxReasoningSteps: 30}}
	e := NewTurnEngine(a, EnginePolicy{})
	if e.Awareness().ShouldRecap() {
		t.Fatalf("recap should be disabled for negative interval")
	}
	for i := 0; i < 10; i++ {
		e.BeginTurn()
	}
	if e.Awareness().Turn != 10 {
		t.Fatalf("turn counter not tracked: %d", e.Awareness().Turn)
	}
}

func TestSubagentRecapSharesEngine(t *testing.T) {
	a := &Agent{Config: &config.Config{RecapInterval: 5, MaxReasoningSteps: 30, RepeatGuardLimit: 10}}
	e := NewTurnEngine(a, EnginePolicy{AgentName: "tester", SessionKey: "subagent:tester"})
	e.BeginTurn()
	e.BeginTurn()
	e.BeginTurn()
	e.BeginTurn()
	e.BeginTurn()
	if !e.Awareness().ShouldRecap() {
		t.Fatalf("subagent should recap at turn 5")
	}
	msg := e.Awareness().Format("tester", 1200, 800, 32768)
	if !strings.Contains(msg, "turn 5/30") || !strings.Contains(msg, "next recap in 5 turn") {
		t.Fatalf("bad recap format: %s", msg)
	}
	if !strings.Contains(e.HaltNotice(), "tester halted") {
		t.Fatalf("halt notice must name the agent: %s", e.HaltNotice())
	}
}
