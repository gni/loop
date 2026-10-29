package agent

import (
	"strings"
	"testing"
)

func TestEditStreamsOldAndNewTextBeforePath(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.PreviewOnly()
	var received []string
	var titles []string
	s.SetLiveContentCallback(func(i int, name, path, delta string) {
		if i != 0 || name != "edit" {
			t.Fatalf("wrong edit identity: %d %q", i, name)
		}
		titles = append(titles, path)
		received = append(received, delta)
	})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 7, Content: "edit"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `{"updates":[{"oldText":"old`})
	if got := strings.Join(received, ""); got != "- old" {
		t.Fatalf("old text did not stream immediately: %q", got)
	}
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `\nline","newText":"new\n`})
	if got := strings.Join(received, ""); got != "- old\n- line\n+ new\n" {
		t.Fatalf("missing or reordered edit lines: %q", got)
	}
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `line"}],"path":"fixed.go"}`})
	if got := strings.Join(received, ""); got != "- old\n- line\n+ new\n+ line\n" {
		t.Fatalf("incorrect final edit preview: %q", got)
	}
	if titles[len(titles)-1] != "fixed.go" {
		t.Fatalf("late target not propagated: %q", titles)
	}
	if len(r.events) != 0 {
		t.Fatalf("edit fragments must not be replayed: %q", r.events)
	}
	s.Replay(0)
	if len(r.events) != 0 {
		t.Fatalf("edit was replayed: %q", r.events)
	}
}

func TestMixedWriteEditReadOnlyGeneratesEditablePreviews(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.PreviewOnly()
	var seen []string
	s.SetLiveContentCallback(func(index int, name, path, delta string) {
		if name == "write" || name == "edit" {
			seen = append(seen, name+":"+delta)
		}
	})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 0, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 0, Content: `{"content":"poem"}`})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 1, Content: "edit"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 1, Content: `{"oldText":"a","newText":"b"}`})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 2, Content: "read"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 2, Content: `{"path":"poem.txt"}`})
	joined := strings.Join(seen, "|")
	if !strings.Contains(joined, "write:poem") || !strings.Contains(joined, "edit:- a") || !strings.Contains(joined, "+ b") {
		t.Fatalf("missing live content: %q", joined)
	}
	if len(r.events) != 0 {
		t.Fatalf("non-body calls should be deferred until execution: %q", r.events)
	}
	s.Replay(2)
	if len(r.events) == 0 {
		t.Fatal("read call must be rendered when executing")
	}
}
