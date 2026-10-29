package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestRendererStreamsSecondWriteBeforeFinalFlush(t *testing.T) {
	var out bytes.Buffer
	renderer := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(`{"path":"first.md","content":"First file"}`)
	renderer.StartToolCall("write", 1)
	renderer.WriteToolCall(`{"content":"Second`)

	partial := style.StripAnsi(out.String())
	if !strings.Contains(partial, "◆ write\nSecond") {
		t.Fatalf("second write must stream without path or closing JSON: %q", partial)
	}
	if !renderer.DidStreamToolBody(1) {
		t.Fatal("second write body must be marked streamed")
	}

	renderer.WriteToolCall(` file","path":"second.md"}`)
	renderer.Flush()
	got := style.StripAnsi(out.String())
	if strings.Count(got, "◆ write") != 2 || strings.Count(got, "Second file") != 1 {
		t.Fatalf("multi-write output duplicated or missing: %q", got)
	}
	if !strings.Contains(got, "◆ write second.md\nSecond file") {
		t.Fatalf("second write path should replace provisional title: %q", got)
	}
}

func TestRendererSeparatesAdjacentWritePreviews(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string
		want  string
	}{
		{
			name:  "no trailing newline",
			first: `{"path":"first.md","content":"First body"}`,
			want:  "First body\n⎿  writing...\n\n◆ write second.md\n",
		},
		{
			name:  "existing trailing newline",
			first: `{"path":"first.md","content":"First body\n"}`,
			want:  "First body\n⎿  writing...\n\n◆ write second.md\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
			r.StartToolCall("write", 0)
			r.WriteToolCall(tc.first)
			r.StartToolCall("write", 1)
			r.WriteToolCall(`{"path":"second.md","content":"Second body"}`)
			r.Flush()
			plain := style.StripAnsi(out.String())
			if !strings.Contains(plain, tc.want) {
				t.Fatalf("adjacent previews not separated by one blank row: %q", plain)
			}
			if strings.Count(plain, "Second body") != 1 {
				t.Fatalf("second body missing or duplicated: %q", plain)
			}
		})
	}
}

func TestRendererMatchesInlineResultsToWriteIndexes(t *testing.T) {
	var out bytes.Buffer
	r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	r.StartToolCall("write", 0)
	r.WriteToolCall(`{"path":"harbor.txt","content":"Harbor"}`)
	r.StartToolCall("write", 1)
	r.WriteToolCall(`{"path":"library.txt","content":"Library"}`)
	r.Flush()
	if !r.TryCompleteWriteResult(1, "⎿  library.txt (12 bytes)") {
		t.Fatal("cannot replace second result")
	}
	if !r.TryCompleteWriteResult(0, "⎿  harbor.txt (6 bytes)") {
		t.Fatal("cannot replace first result")
	}
	got := style.StripAnsi(out.String())
	first := "◆ write harbor.txt\nHarbor\n⎿  harbor.txt (6 bytes)"
	second := "◆ write library.txt\nLibrary\n⎿  library.txt (12 bytes)"
	if !strings.Contains(got, first) || !strings.Contains(got, second) {
		t.Fatalf("result not matched to preview: %q", got)
	}
	if strings.Index(got, first) >= strings.Index(got, second) {
		t.Fatalf("preview order changed: %q", got)
	}
}

func TestRendererClearsPendingWriteResultAfterInterruption(t *testing.T) {
	var out bytes.Buffer
	r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	r.StartToolCall("write", 0)
	r.WriteToolCall(`{"path":"a.md","content":"A"}`)
	r.Flush()
	if !r.HasPendingWriteResult(0) {
		t.Fatal("expected reserved result")
	}
	r.MarkPendingWriteResults("Not executed")
	if r.HasPendingWriteResult(0) {
		t.Fatal("pending result was not cleared")
	}
	if !strings.Contains(style.StripAnsi(out.String()), "A\n⎿  Not executed") {
		t.Fatalf("wrong status: %q", out.String())
	}
}

func TestRendererAnnouncesWriteBeforeFilenameAndContent(t *testing.T) {
	var out bytes.Buffer
	r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	r.StartToolCall("write", 0)
	if got := style.StripAnsi(out.String()); !strings.Contains(got, "◆ write\n") {
		t.Fatalf("missing provisional title before filename: %q", got)
	}
	r.WriteToolCall(`{"content":"A poem starts`)
	if got := style.StripAnsi(out.String()); !strings.Contains(got, "A poem starts") {
		t.Fatalf("content was buffered without filename: %q", got)
	}
	r.WriteToolCall(` here","path":"poem.txt"}`)
	r.Flush()
	if got := style.StripAnsi(out.String()); strings.Count(got, "◆ write") != 1 || !strings.Contains(got, "A poem starts here") {
		t.Fatalf("provisional write was duplicated or lost: %q", got)
	}
}
