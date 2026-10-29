package agent

import (
	"strings"
	"testing"
)

func TestLivePreviewStreamsEveryCallBeforeExecution(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.PreviewOnly()
	s.DeferFollowingCalls()
	type progress struct {
		index      int
		name, body string
	}
	var updates []progress
	s.SetProgressCallback(func(index int, name, body string, _ int) {
		updates = append(updates, progress{index, name, body})
	})
	for _, ch := range []StreamChunk{
		{Type: "tool_name", Content: "write", ToolCallIndex: 0},
		{Type: "tool_call", Content: `{"content":"First line\nSe`, ToolCallIndex: 0},
		{Type: "tool_name", Content: "write", ToolCallIndex: 3},
		{Type: "tool_call", Content: `{"content":"Second line\nNow`, ToolCallIndex: 3},
		{Type: "tool_call", Content: ` visible","path":"second.md"}`, ToolCallIndex: 3},
	} {
		s.Feed(ch)
	}
	if len(r.events) != 0 {
		t.Fatalf("live preview must not emit unvalidated permanent output: %q", r.events)
	}
	var sawFirst, sawSecond, sawPath bool
	for _, u := range updates {
		sawFirst = sawFirst || u.index == 0 && strings.Contains(u.body, "First line\nSe")
		sawSecond = sawSecond || u.index == 1 && strings.Contains(u.body, "Second line\nNow")
		sawPath = sawPath || u.index == 1 && strings.Contains(u.name, "second.md")
	}
	if !sawFirst || !sawSecond || !sawPath {
		t.Fatalf("missing live file content or filename: %#v", updates)
	}
	s.Finish()
	if len(r.events) != 0 {
		t.Fatal("Finish must not print speculative tool bodies")
	}
	s.Replay(1)
	if len(r.events) == 0 || r.events[0] != "start:write" {
		t.Fatalf("execution replay missing: %q", r.events)
	}
}

func TestLivePreviewBeforeToolName(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.PreviewOnly()
	var first string
	s.SetProgressCallback(func(_ int, name, body string, _ int) {
		if first == "" {
			first = body
		}
	})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `{"content":"hello`})
	if first != "hello" {
		t.Fatalf("unnamed content was held: %q", first)
	}
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 7, Content: "write"})
}
