package agent

import (
	"strings"
	"testing"
)

func TestEveryWriteStreamsBeforeInferenceCompletes(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.PreviewOnly()
	var seen []string
	s.SetLiveContentCallback(func(i int, name, path, delta string) {
		if name == "write" && delta != "" {
			seen = append(seen, name+":"+path+":"+delta)
		}
	})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"content":"First`, ToolCallIndex: 0})
	if len(seen) != 1 || !strings.Contains(seen[0], "First") {
		t.Fatalf("first content not live: %q", seen)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: ` line","path":"a.txt"}`, ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 1})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"content":"Second\nline`, ToolCallIndex: 1})
	if len(seen) < 3 || !strings.Contains(strings.Join(seen, ""), "Second\nline") {
		t.Fatalf("second write blocked: %q", seen)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: `","path":"b.txt"}`, ToolCallIndex: 1})
	if len(r.events) != 0 {
		t.Fatalf("preview path must not replay into legacy renderer: %v", r.events)
	}
}
