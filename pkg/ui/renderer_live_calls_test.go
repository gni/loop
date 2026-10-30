package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestWriteStreamStartsBeforeFilenameAndStreamsSecondCall(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "write", "", "First\nLine one")
	r.ShowLiveToolContent(0, "write", "first.md", " continued\n")
	r.ShowLiveToolContent(1, "write", "", "Second\nLine two")
	r.ShowLiveToolContent(1, "write", "second.md", " ends")
	r.EndLiveToolContent()
	output := style.StripAnsi(b.String())
	for _, text := range []string{"✚ write first.md", "First\nLine one continued", "✚ write second.md", "Second\nLine two ends"} {
		if !strings.Contains(output, text) {
			t.Fatalf("missing %q in %q", text, output)
		}
	}
	if strings.Contains(output, "receiving") || strings.Contains(output, "writing...") {
		t.Fatalf("stale placeholders: %q", output)
	}
	// The middle empty row is reserved for the first write's result.
	// The following row must remain blank after that result is filled.
	if !strings.Contains(output, "First\nLine one continued\n\n\n✚ write second.md\nSecond") {
		t.Fatalf("missing blank row between completed writes: %q", output)
	}
	if r.GetToolTitleLineNumber(1) < 0 || !r.DidStreamToolBody(1) {
		t.Fatal("renderer lost identity of second call")
	}
}

func TestWriteStreamRejectsTerminalControlInTarget(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "write", "evil\x1b[2J\nfoo", "text")
	if strings.Contains(b.String(), "\x1b[2J") {
		t.Fatal("unsafe terminal controls")
	}
}

func TestWriteCompletionOccupiesOwnBlankRowWithoutPlaceholder(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.SetSequentialToolResults(true)
	r.ShowLiveToolContent(0, "write", "a.txt", "First\n")
	r.ShowLiveToolContent(1, "write", "b.txt", "Second")
	r.EndLiveToolContent()
	if !r.HasPendingWriteResult(0) || !r.HasPendingWriteResult(1) {
		t.Fatal("one slot per streamed call expected")
	}
	if !r.TryCompleteWriteResult(1, "⎿  b.txt (6 bytes)") {
		t.Fatal("second result could not be filled")
	}
	if !r.TryCompleteWriteResult(0, "⎿  a.txt (6 bytes)") {
		t.Fatal("first result could not be filled")
	}
	out := style.StripAnsi(b.String())
	expected := []string{
		"First\n⎿  a.txt (6 bytes)",
		"Second\n⎿  b.txt (6 bytes)",
	}
	for _, item := range expected {
		if !strings.Contains(out, item) {
			t.Fatalf("missing grouped output %q in %q", item, out)
		}
	}
	if !strings.Contains(out, "⎿  a.txt (6 bytes)\n\n✚ write b.txt") {
		t.Fatalf("write results must be followed by a blank separator: %q", out)
	}
}
