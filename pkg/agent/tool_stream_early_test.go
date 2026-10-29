package agent

import (
	"reflect"
	"testing"
)

// A complete JSON object is an execution boundary; partial JSON and duplicate
// stream fragments must never schedule execution.
func TestToolStreamOnReadyOnlyForCompleteArguments(t *testing.T) {
	s := newToolStreamSequencer(&toolStreamRecorder{}, true)
	s.PreviewOnly()
	type ready struct {
		idx        int
		name, args string
	}
	var got []ready
	s.SetOnReady(func(index int, name, arguments string) {
		got = append(got, ready{index, name, arguments})
	})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 7, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `{"content":"some poem`})
	if len(got) != 0 {
		t.Fatalf("executed incomplete JSON: %+v", got)
	}
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: `","path":"poem.txt"}`})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 7, Content: "   "})
	want := []ready{{0, "write", `{"content":"some poem","path":"poem.txt"}`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ready callbacks = %+v, want %+v", got, want)
	}
}

func TestToolStreamOnReadyPreservesInterleavedCallIDs(t *testing.T) {
	s := newToolStreamSequencer(&toolStreamRecorder{}, true)
	s.PreviewOnly()
	var indexes []int
	s.SetOnReady(func(index int, name, arguments string) { indexes = append(indexes, index) })
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 3, Content: "edit"})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 9, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 9, Content: `{"path":"two","content":"b"}`})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 3, Content: `{"path":"one","updates":[]}`})
	if !reflect.DeepEqual(indexes, []int{1, 0}) {
		t.Fatalf("index mapping = %v", indexes)
	}
}

func TestToolStreamOnReadyRejectsInvalidJSON(t *testing.T) {
	s := newToolStreamSequencer(&toolStreamRecorder{}, true)
	s.PreviewOnly()
	n := 0
	s.SetOnReady(func(int, string, string) { n++ })
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 0, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 0, Content: `{"path":"a",}`})
	if n != 0 {
		t.Fatal("accepted malformed JSON")
	}
}

func TestToolStreamCompletedCallRunsBeforeNextPreview(t *testing.T) {
	s := newToolStreamSequencer(&toolStreamRecorder{}, true)
	s.PreviewOnly()
	var events []string
	s.SetLiveContentCallback(func(index int, name, path, delta string) {
		if delta != "" {
			events = append(events, name+" "+delta)
		}
	})
	s.SetOnReady(func(index int, name, arguments string) {
		events = append(events, "verified result")
	})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 0, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 0, Content: `{"path":"a.txt","content":"One"}`})
	s.Feed(StreamChunk{Type: "tool_name", ToolCallIndex: 1, Content: "write"})
	s.Feed(StreamChunk{Type: "tool_call", ToolCallIndex: 1, Content: `{"path":"b.txt","content":"Two"}`})
	if !reflect.DeepEqual(events, []string{"write One", "verified result", "write Two", "verified result"}) {
		t.Fatalf("wrong live order: %#v", events)
	}
}
