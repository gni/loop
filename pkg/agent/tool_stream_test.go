package agent

import (
	"reflect"
	"testing"
)

type toolStreamRecorder struct {
	StreamRenderer
	current int
	events  []string
}

func (r *toolStreamRecorder) StartToolCall(name string, index int) {
	r.current = index
	r.events = append(r.events, "start:"+name)
}

func (r *toolStreamRecorder) SetToolTarget(target string, index int) {
	if index != r.current {
		panic("target forwarded to wrong tool")
	}
	r.events = append(r.events, "target:"+target)
}

func (r *toolStreamRecorder) WriteToolCall(content string) {
	r.events = append(r.events, "args:"+content)
}

func (r *toolStreamRecorder) Flush() {}

func TestToolStreamMultipleWritesWithoutWaitingForResponseEnd(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"first.md","content":"a"}`, ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"second.md","content":"b`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	want := []string{
		"start:write", `args:{"path":"first.md","content":"a"}`,
		"start:write", `args:{"path":"second.md","content":"b`,
	}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("second write must stream before Finish: got %q, want %q", r.events, want)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: `"}`, ToolCallIndex: 1})
	if got := r.events[len(r.events)-1]; got != `args:"}` {
		t.Fatalf("second write ceased streaming: %q", got)
	}
}

func TestToolStreamInterleavedWritesStayInOrder(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"first.md","content":"A`, ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"second.md","content":"B`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	if len(r.events) != 2 {
		t.Fatalf("second write must be held until first JSON completes: %q", r.events)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: `"}`, ToolCallIndex: 0})
	want := []string{
		"start:write", `args:{"path":"first.md","content":"A`, `args:"}`,
		"start:write", `args:{"path":"second.md","content":"B`,
	}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("pending second write must replay and continue streaming: got %q, want %q", r.events, want)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: `"}`, ToolCallIndex: 1})
	if got := r.events[len(r.events)-1]; got != `args:"}` {
		t.Fatalf("second stream continuation lost: %q", r.events)
	}
}

func TestToolStreamIncompleteFirstCallReplaysSecondAtEnd(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"content":"unterminated`, ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"second.md","content":"ok"}`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	if len(r.events) != 2 {
		t.Fatalf("later output was interleaved: %q", r.events)
	}
	s.Finish()
	if len(r.events) != 4 || r.events[2] != "start:write" {
		t.Fatalf("buffered second write lost at end: %q", r.events)
	}
}

func TestToolStreamTargetsAndDisabledMode(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_target", Content: "poem.md", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_call", Content: "plain text", ToolCallIndex: 0})
	if !reflect.DeepEqual(r.events, []string{"start:write", "target:poem.md", "args:plain text"}) {
		t.Fatalf("fallback target or body lost: %q", r.events)
	}
	s2 := newToolStreamSequencer(r, false)
	s2.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 1})
	s2.Finish()
	if len(r.events) != 3 {
		t.Fatalf("disabled streaming still emitted output: %q", r.events)
	}
}

func TestToolStreamFallbackRawWritesAdvanceOnNextTool(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_target", Content: "first.md", ToolCallIndex: 0},
		{Type: "tool_call", Content: "first body", ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_target", Content: "second.md", ToolCallIndex: 1},
		{Type: "tool_call", Content: "second body", ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	want := []string{
		"start:write", "target:first.md", "args:first body",
		"start:write", "target:second.md", "args:second body",
	}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("fallback second body should stream immediately: got %q, want %q", r.events, want)
	}
}

func TestToolStreamJSONCompletionIgnoresBracesInsideStrings(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 1})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"path":"one.md","content":"brace } and escaped \" quote`, ToolCallIndex: 0})
	if len(r.events) != 2 {
		t.Fatalf("first tool falsely treated as complete: %q", r.events)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: `"}`, ToolCallIndex: 0})
	if len(r.events) != 4 || r.events[2] != "args:\"}" || r.events[3] != "start:write" {
		t.Fatalf("nested JSON completion failed: %q", r.events)
	}
}

func TestToolStreamSparseProviderIndexesNormalizeForExecution(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 2},
		{Type: "tool_call", Content: `{"path":"one.md","content":"one"}`, ToolCallIndex: 2},
		{Type: "tool_name", Content: "write", ToolCallIndex: 5},
		{Type: "tool_target", Content: "two.md", ToolCallIndex: 5},
		{Type: "tool_call", Content: "two", ToolCallIndex: 5},
	} {
		s.Feed(event)
	}
	want := []string{
		"start:write", `args:{"path":"one.md","content":"one"}`,
		"start:write", "target:two.md", "args:two",
	}
	if !reflect.DeepEqual(r.events, want) || r.current != 1 {
		t.Fatalf("native provider indexes were not normalized: events=%q current=%d", r.events, r.current)
	}
}
