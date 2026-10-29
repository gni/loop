package agent

import (
	"reflect"
	"testing"
)

// A completed first call is streamed, while the second call stays invisible
// until the engine reaches it, even if provider deltas are interleaved.
func TestSequentialDisplayReplaysEachToolAtExecution(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"one.md","content":"first"}`, ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"two.md","content":"second"}`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	s.Finish()
	first := []string{"start:write", `args:{"path":"one.md","content":"first"}`}
	if !reflect.DeepEqual(r.events, first) {
		t.Fatalf("later preview rendered before execution: %q", r.events)
	}
	s.Replay(1)
	expected := append(first, "start:write", `args:{"path":"two.md","content":"second"}`)
	if !reflect.DeepEqual(r.events, expected) {
		t.Fatalf("replay lost or mixed tool calls: %q", r.events)
	}
	s.Replay(1)
	if !reflect.DeepEqual(r.events, expected) {
		t.Fatalf("replay duplicated tool body: %q", r.events)
	}
}

func TestSequentialDisplayBuffersOtherInspectionTools(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "read", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"a.txt"}`, ToolCallIndex: 0},
		{Type: "tool_name", Content: "read", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"b.txt"}`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	s.Finish()
	if len(r.events) != 2 {
		t.Fatalf("read headers should not be grouped: %q", r.events)
	}
	s.Replay(1)
	if len(r.events) != 4 || r.events[2] != "start:read" {
		t.Fatalf("second read was not deferred: %q", r.events)
	}
}

func TestSequentialDisplayProviderSparseIndexes(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 3},
		{Type: "tool_call", Content: `{"path":"a","content":"first"}`, ToolCallIndex: 3},
		{Type: "tool_name", Content: "write", ToolCallIndex: 8},
		{Type: "tool_call", Content: `{"path":"b","content":"second"}`, ToolCallIndex: 8},
	} {
		s.Feed(event)
	}
	s.Finish()
	if len(r.events) != 2 {
		t.Fatalf("unexpected preview before replay: %q", r.events)
	}
	s.Replay(1)
	if len(r.events) != 4 || r.events[3] != `args:{"path":"b","content":"second"}` {
		t.Fatalf("sparse source indexes were not mapped: %q", r.events)
	}
}

func TestSequentialDisplayResultAlwaysPrecedesFollowingPreview(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	for _, event := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"path":"one.txt","content":"one"}`, ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 1},
		{Type: "tool_call", Content: `{"path":"two.txt","content":"two"}`, ToolCallIndex: 1},
	} {
		s.Feed(event)
	}
	s.Finish()
	// The engine completes the first call before invoking Replay(1).
	r.events = append(r.events, "result:one.txt")
	s.Replay(1)
	r.events = append(r.events, "result:two.txt")
	want := []string{
		"start:write", `args:{"path":"one.txt","content":"one"}`,
		"result:one.txt",
		"start:write", `args:{"path":"two.txt","content":"two"}`,
		"result:two.txt",
	}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("results detached from their previews: %q", r.events)
	}
}
