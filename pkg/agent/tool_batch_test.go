package agent

import (
	"context"
	"io"
	"reflect"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestToolBatchLifecycleStableIDsAndOrder(t *testing.T) {
	var updates []ToolLifecycleEvent
	batch := NewToolBatch(func(event ToolLifecycleEvent) { updates = append(updates, event) })
	batch.Observe(0, "write", `{"path":"a.txt",`)
	batch.Observe(1, "read", `{"path":"b.txt"}`)
	initial, err := batch.Snapshot(0)
	if err != nil || initial.CallID == "" {
		t.Fatalf("missing provisional call ID: %+v %v", initial, err)
	}
	batch.Observe(0, "", `"content":"hello"}`)
	calls := []db.ToolCall{
		{ID: "provider-a", Function: db.ToolFunction{Name: "write", Arguments: `{"path":"a.txt","content":"hello"}`}},
		{ID: "provider-b", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"b.txt"}`}},
	}
	batch.Seal(calls)
	batch.Start(0)
	batch.Finish(0, false)
	batch.Start(1)
	batch.Finish(1, true)
	a, err := batch.Snapshot(0)
	if err != nil || a.CallID != initial.CallID || a.ProviderID != "provider-a" || a.Phase != ToolPhaseCompleted {
		t.Fatalf("lost call identity: %+v %v", a, err)
	}
	b, err := batch.Snapshot(1)
	if err != nil || b.CallID == a.CallID || b.ProviderID != "provider-b" || b.Phase != ToolPhaseFailed {
		t.Fatalf("mixed call state: %+v %v", b, err)
	}
	var transitions []ToolPhase
	for _, event := range updates {
		if event.Index == 1 {
			transitions = append(transitions, event.Phase)
		}
	}
	if !reflect.DeepEqual(transitions, []ToolPhase{ToolPhaseStreaming, ToolPhaseReady, ToolPhaseRunning, ToolPhaseFailed}) {
		t.Fatalf("unexpected lifecycle: %v", transitions)
	}
}

func TestToolBatchCancellationAndRedaction(t *testing.T) {
	batch := NewToolBatch(nil)
	batch.Observe(0, "bash", `{"command":"echo PRIVATE_KEY"}`)
	batch.Seal([]db.ToolCall{{ID: "id", Function: db.ToolFunction{Name: "bash", Arguments: `{"command":"echo PRIVATE_KEY"}`}}})
	batch.CancelByProviderID("id")
	batch.Finish(0, false)
	call, err := batch.Snapshot(0)
	if err != nil || call.Phase != ToolPhaseCancelled {
		t.Fatalf("cancelled call changed state: %+v %v", call, err)
	}
	if call.ArgumentBytes == 0 {
		t.Fatal("missing streamed length")
	}
}

type sequenceReadModel struct{ calls int }

func (m *sequenceReadModel) CheckThinkingSupport(context.Context) bool { return false }
func (m *sequenceReadModel) StreamChatCompletions(ctx context.Context, _ []db.Message, _ []tool.Tool, chunks chan<- StreamChunk) (*db.Message, error) {
	m.calls++
	if m.calls == 1 {
		chunks <- StreamChunk{Type: "tool_name", Content: "read", ToolCallIndex: 0}
		chunks <- StreamChunk{Type: "tool_call", Content: `{"path":"a"}`, ToolCallIndex: 0}
		chunks <- StreamChunk{Type: "tool_name", Content: "read", ToolCallIndex: 1}
		chunks <- StreamChunk{Type: "tool_call", Content: `{"path":"b"}`, ToolCallIndex: 1}
		return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{
			{ID: "read-a", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"a"}`}},
			{ID: "read-b", Type: "function", Function: db.ToolFunction{Name: "read", Arguments: `{"path":"b"}`}},
		}}, nil
	}
	return &db.Message{Role: "assistant", Content: "complete"}, nil
}

type orderReadTool struct{ got *[]string }

func (x orderReadTool) Name() string { return "read" }
func (x orderReadTool) Definition() tool.Tool {
	return tool.NewFunctionTool("read", "inspect", map[string]tool.SchemaProp{"path": tool.StringProp("path")}, "path")
}
func (x orderReadTool) Execute(_ tool.AgentContext, args string) (string, error) {
	*x.got = append(*x.got, args)
	return "ok", nil
}

func TestBatchedToolsExecuteSequentiallyWithOneInference(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.StreamWrites = true
	cfg.ParallelToolCalls = false
	cfg.MaxReasoningSteps = 3
	model := &sequenceReadModel{}
	a := NewAgentAt(cfg, "", nil, t.TempDir())
	got := []string{}
	a.Registry.Register(orderReadTool{got: &got})
	a.LLMProvider = model
	messages := []db.Message{}
	_, err := a.RunAgentLoop(context.Background(), io.Discard, &messages, "read two files", []string{"read"}, style.GetTheme("plain"), true, "")
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("expected one tool batch and one follow-up inference, got %d", model.calls)
	}
	if !reflect.DeepEqual(got, []string{`{"path":"a"}`, `{"path":"b"}`}) {
		t.Fatalf("wrong execution order: %q", got)
	}
	var ids []string
	for _, message := range messages {
		if message.Role == "tool" {
			ids = append(ids, message.ToolCallID)
		}
	}
	if !reflect.DeepEqual(ids, []string{"read-a", "read-b"}) {
		t.Fatalf("tool results mismatched: %q", ids)
	}
}
