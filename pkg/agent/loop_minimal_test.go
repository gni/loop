package agent

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type scriptedModel struct {
	count    atomic.Int32
	toolOnce bool
}

func (s *scriptedModel) CheckThinkingSupport(context.Context) bool { return false }
func (s *scriptedModel) StreamChatCompletions(ctx context.Context, _ []db.Message, _ []tool.Tool, _ chan<- StreamChunk) (*db.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := s.count.Add(1)
	if s.toolOnce && n == 1 {
		return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{ID: "call-1", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: "{}"}}}}, nil
	}
	return &db.Message{Role: "assistant", Content: "done"}, nil
}

type fakeReadTool struct{}

func (fakeReadTool) Name() string { return "read" }
func (fakeReadTool) Definition() tool.Tool {
	return tool.NewFunctionTool("read", "No-op test read", nil)
}
func (fakeReadTool) Execute(_ tool.AgentContext, _ string) (string, error) { return "file", nil }

func TestMinimalAgentLoopCallsModelOnlyWhenNeeded(t *testing.T) {
	for _, toolOnce := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.AutoApprove = false
		cfg.MaxReasoningSteps = 3
		a := NewAgentAt(cfg, "", nil, t.TempDir())
		a.Registry.Register(fakeReadTool{})
		model := &scriptedModel{toolOnce: toolOnce}
		a.LLMProvider = model
		messages := []db.Message{}
		result, err := a.RunAgentLoop(context.Background(), io.Discard, &messages, "hi", []string{"read"}, style.GetTheme("plain"), true, "")
		if err != nil {
			t.Fatalf("toolOnce=%t: %v", toolOnce, err)
		}
		want := int32(1)
		if toolOnce {
			want = 2
		}
		if got := model.count.Load(); got != want {
			t.Errorf("toolOnce=%t: calls=%d want=%d", toolOnce, got, want)
		}
		if result.Turns != int(want) {
			t.Errorf("toolOnce=%t: turns=%d want=%d", toolOnce, result.Turns, want)
		}
	}
}
