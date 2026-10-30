package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type writeDisplayProbe struct{ executions int }

func (p *writeDisplayProbe) Name() string { return "write" }

func (p *writeDisplayProbe) Definition() tool.Tool {
	return tool.NewFunctionTool("write", "test write", map[string]tool.SchemaProp{
		"path":    tool.StringProp("file path"),
		"content": tool.StringProp("file content"),
	}, "path", "content")
}

func (p *writeDisplayProbe) Execute(_ tool.AgentContext, _ string) (string, error) {
	p.executions++
	return "Successfully wrote 5 bytes to poem.md", nil
}

type writeDisplayHistory struct{ messages []db.Message }

func (h *writeDisplayHistory) Append(m db.Message)    { h.messages = append(h.messages, m) }
func (h *writeDisplayHistory) Messages() []db.Message { return h.messages }

func TestWriteShowsOneTitleAndRunsOnce(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.StreamWrites = true
	cfg.CollapseResults = true
	cfg.AutoApprove = true
	theme := style.GetTheme("plain")
	a := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	a.UI = &AgentUIImpl{Config: cfg, Theme: theme}
	probe := &writeDisplayProbe{}
	a.Registry.Register(probe)

	var output bytes.Buffer
	writer := agent.NewNewlineCounterWriter(&output)
	sr := NewStreamRenderer(writer, theme, false, cfg.StreamWrites, "")
	sink := &writeDisplayHistory{}
	msg := &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{
		ID: "write-1", Type: "function",
		Function: db.ToolFunction{Name: "write", Arguments: `{"path":"poem.md","content":"hello"}`},
	}}}

	halted, aborted := agent.NewTurnEngine(a, agent.EnginePolicy{}).ExecuteToolCalls(
		context.Background(), msg, sink, sr, nil, writer, theme, agent.NewMainRenderer(a), nil,
	)
	if halted || aborted {
		t.Fatalf("write unexpectedly stopped: halted=%t aborted=%t", halted, aborted)
	}
	if got := probe.executions; got != 1 {
		t.Fatalf("write executions = %d; want 1", got)
	}
	if got := strings.Count(style.StripAnsi(output.String()), "✚ write "); got != 1 {
		t.Fatalf("write titles = %d; want 1; output = %q", got, style.StripAnsi(output.String()))
	}
	if len(sink.messages) != 1 || sink.messages[0].Role != "tool" || sink.messages[0].IsError {
		t.Fatalf("unexpected tool results: %+v", sink.messages)
	}
}

func TestMalformedWriteArgumentsDoNotExecute(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AutoApprove = true
	theme := style.GetTheme("plain")
	a := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	a.UI = &AgentUIImpl{Config: cfg, Theme: theme}
	probe := &writeDisplayProbe{}
	a.Registry.Register(probe)

	var output bytes.Buffer
	writer := agent.NewNewlineCounterWriter(&output)
	sr := NewStreamRenderer(writer, theme, false, true, "")
	sink := &writeDisplayHistory{}
	msg := &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{
		ID: "write-1", Type: "function",
		Function: db.ToolFunction{Name: "write", Arguments: `{"path":"poem.md","content":`},
	}}}

	agent.NewTurnEngine(a, agent.EnginePolicy{}).ExecuteToolCalls(
		context.Background(), msg, sink, sr, nil, writer, theme, agent.NewMainRenderer(a), nil,
	)
	if probe.executions != 0 {
		t.Fatalf("malformed arguments executed %d times", probe.executions)
	}
	if len(sink.messages) != 1 || !sink.messages[0].IsError {
		t.Fatalf("expected one rejected result, got %+v", sink.messages)
	}
}

type streamingWriteModel struct{ calls int }

func (m *streamingWriteModel) CheckThinkingSupport(context.Context) bool { return false }

func (m *streamingWriteModel) StreamChatCompletions(ctx context.Context, _ []db.Message, _ []tool.Tool, out chan<- agent.StreamChunk) (*db.Message, error) {
	m.calls++
	if m.calls > 1 {
		return &db.Message{Role: "assistant", Content: "done"}, nil
	}
	for _, chunk := range []agent.StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"poem.md","content":"hel`, ToolCallIndex: 0},
		{Type: "tool_call", Content: `lo"}`, ToolCallIndex: 0},
	} {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case out <- chunk:
		}
	}
	return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{
		ID: "write-stream", Type: "function",
		Function: db.ToolFunction{Name: "write", Arguments: `{"path":"poem.md","content":"hello"}`},
	}}}, nil
}

func TestWriteStreamsContentDuringGenerationWithoutDuplicateTitle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.StreamWrites = true
	cfg.CollapseResults = false
	cfg.AutoApprove = true
	cfg.MaxReasoningSteps = 3
	cfg.DisableRecap = true
	theme := style.GetTheme("plain")
	a := agent.NewAgentAt(cfg, "", nil, t.TempDir())
	a.UI = &AgentUIImpl{Config: cfg, Theme: theme}
	model := &streamingWriteModel{}
	a.LLMProvider = model
	probe := &writeDisplayProbe{}
	a.Registry.Register(probe)

	var output bytes.Buffer
	messages := []db.Message{}
	_, err := a.RunAgentLoop(context.Background(), &output, &messages, "write the poem", []string{"write"}, theme, true, "")
	if err != nil {
		t.Fatal(err)
	}
	plain := style.StripAnsi(output.String())
	if got := strings.Count(plain, "✚ write "); got != 1 {
		t.Fatalf("tool title appeared %d times, output: %q", got, plain)
	}
	if got := strings.Count(plain, "hello"); got != 1 {
		t.Fatalf("streamed content appeared %d times, output: %q", got, plain)
	}
	if probe.executions != 1 || model.calls != 2 {
		t.Fatalf("unexpected executions: write=%d model=%d", probe.executions, model.calls)
	}
}
