package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeferredWriteReportsBodyBeforeFilename(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	var excerpts []string
	s.SetProgressCallback(func(_ int, name, text string, _ int) {
		if name == "write" && text != "" {
			excerpts = append(excerpts, text)
		}
	})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"path":"first.txt","content":"first"}`, ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 1})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"content":"The night`, ToolCallIndex: 1})
	if len(excerpts) == 0 || excerpts[len(excerpts)-1] != "The night" {
		t.Fatalf("missing live content-first preview: %q", excerpts)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: ` turns to day","path":"second.txt"}`, ToolCallIndex: 1})
	if got := excerpts[len(excerpts)-1]; got != "The night turns to day" {
		t.Fatalf("content preview lost when path came later: %q", got)
	}
	if len(r.events) != 2 {
		t.Fatalf("deferred preview entered permanent transcript: %q", r.events)
	}
	s.Finish()
	s.Replay(1)
	if got := r.events[len(r.events)-1]; !strings.Contains(got, "second.txt") {
		t.Fatalf("final write was not replayed: %q", r.events)
	}
}

func TestUnnamedCallReportsContentBeforeToolName(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	var preview string
	s.SetProgressCallback(func(_ int, name, text string, size int) {
		if name == "" && size > 0 {
			preview = text
		}
	})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"content":"hello`, ToolCallIndex: 3})
	if preview != "hello" {
		t.Fatalf("no provisional preview for tool without name: %q", preview)
	}
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 3})
	s.Feed(StreamChunk{Type: "tool_call", Content: ` world","path":"hello.md"}`, ToolCallIndex: 3})
	if len(r.events) != 3 || r.events[0] != "start:write" {
		t.Fatalf("unnamed fragments did not replay after name arrived: %q", r.events)
	}
}

func TestDraftParserHandlesFragmentBoundariesAndEscapes(t *testing.T) {
	var d streamedDraft
	for _, part := range []string{`{"pa`, `th":"demo.txt","con`, `tent":"line`, `\nmore \"`, `quoted\" end"`} {
		d.appendJSON(part)
	}
	if got := d.preview(); !strings.Contains(got, "line more") || !strings.Contains(got, "quoted") {
		t.Fatalf("wrong draft content %q", got)
	}
	if strings.Contains(d.preview(), "demo.txt") {
		t.Fatalf("path leaked into body preview: %q", d.preview())
	}
}

func TestStreamDraftDoesNotChangeExistingEventOrdering(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	s.SetProgressCallback(func(int, string, string, int) {})
	events := []StreamChunk{{Type: "tool_name", Content: "write", ToolCallIndex: 0}, {Type: "tool_call", Content: `{"path":"a","content":"hello"}`, ToolCallIndex: 0}, {Type: "tool_name", Content: "read", ToolCallIndex: 1}, {Type: "tool_call", Content: `{"path":"a"}`, ToolCallIndex: 1}}
	for _, e := range events {
		s.Feed(e)
	}
	s.Finish()
	if !reflect.DeepEqual(r.events, []string{"start:write", `args:{"path":"a","content":"hello"}`}) {
		t.Fatalf("preview order changed: %q", r.events)
	}
	s.Replay(1)
	if len(r.events) != 4 || r.events[2] != "start:read" {
		t.Fatalf("read replay failed: %q", r.events)
	}
}
