package agent

import "testing"

func TestDeferredProgressWaitsForFirstCallCompletion(t *testing.T) {
	r := &toolStreamRecorder{}
	s := newToolStreamSequencer(r, true)
	s.DeferFollowingCalls()
	updates := 0
	s.SetProgressCallback(func(int, string, string, int) { updates++ })
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"path":"one.md","content":"incomplete`, ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_name", Content: "write", ToolCallIndex: 1})
	s.Feed(StreamChunk{Type: "tool_call", Content: `{"content":"two",`, ToolCallIndex: 1})
	if updates != 0 {
		t.Fatalf("draft interleaved with first write: %d", updates)
	}
	s.Feed(StreamChunk{Type: "tool_call", Content: ` text"}`, ToolCallIndex: 0})
	s.Feed(StreamChunk{Type: "tool_call", Content: `"path":"two.md"}`, ToolCallIndex: 1})
	if updates == 0 {
		t.Fatal("completed first tool did not unblock chat progress")
	}
}
