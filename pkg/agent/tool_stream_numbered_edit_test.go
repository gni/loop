package agent

import (
	"strings"
	"testing"
)

// A numbered diff cannot be computed while oldText or the path is missing.
// The heading is live; complete arguments produce the same formatted diff
// that the edit executor and history renderer use.
func TestEditPreviewUsesCompleteNumberedDiffOnce(t *testing.T) {
	recorder := &toolStreamRecorder{}
	sequencer := newToolStreamSequencer(recorder, true)
	sequencer.PreviewOnly()
	var events []string
	var previewCalls int
	sequencer.SetLiveContentCallback(func(index int, name, path, delta string) {
		if index != 0 || name != "edit" {
			t.Fatalf("unexpected tool: %d %q", index, name)
		}
		events = append(events, path+"|"+delta)
	})
	sequencer.SetEditDiffPreview(func(name, arguments string) string {
		previewCalls++
		if name != "edit" || !strings.Contains(arguments, `"path":"poem.txt"`) {
			t.Fatalf("bad complete edit args: %s", arguments)
		}
		return "    4    before\n\x1b[32m5    + corrected\x1b[0m\n"
	})
	sequencer.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 7, Content: "edit"})
	sequencer.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `{"updates":[{"newText":"correct`})
	if previewCalls != 0 {
		t.Fatal("diff generator called with partial JSON")
	}
	if len(events) == 0 || strings.Contains(strings.Join(events, ""), "correct") {
		t.Fatalf("raw argument leaked: %q", events)
	}
	sequencer.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `ed","oldText":"before"}],"path":"poem.txt"}`})
	if previewCalls != 1 {
		t.Fatalf("diff generated %d times", previewCalls)
	}
	all := strings.Join(events, "\n")
	if !strings.Contains(all, "    4    before") || !strings.Contains(all, "\x1b[32m5    + corrected") {
		t.Fatalf("numbered color diff missing: %q", all)
	}
	if strings.Contains(all, "- before") {
		t.Fatalf("raw edit preview was rendered: %q", all)
	}
	sequencer.Finish()
	sequencer.Replay(0)
	if previewCalls != 1 || len(recorder.events) != 0 {
		t.Fatal("edit preview was duplicated during replay")
	}
}

func TestEditPreviewNoDiffLeavesExecutorToRenderResult(t *testing.T) {
	recorder := &toolStreamRecorder{}
	sequencer := newToolStreamSequencer(recorder, true)
	sequencer.PreviewOnly()
	var events []string
	sequencer.SetLiveContentCallback(func(_ int, _ string, _ string, delta string) { events = append(events, delta) })
	sequencer.SetEditDiffPreview(func(_, _ string) string { return "" })
	sequencer.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 0, Content: "edit"})
	sequencer.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 0, Content: `{"path":"missing.txt","updates":[{"oldText":"abc","newText":"def"}]}`})
	for _, delta := range events {
		if delta != "" {
			t.Fatalf("unverified raw edit leaked: %q", delta)
		}
	}
}
