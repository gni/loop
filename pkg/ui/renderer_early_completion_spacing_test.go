package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

// An early-verified result closes the live preview before the next tool's
// tokens arrive. The next call must still have the same blank separator as
// calls that finish only after the whole model response.
func TestEarlyVerifiedWritesHaveOneBlankSeparator(t *testing.T) {
	var out bytes.Buffer
	r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	for i, file := range []string{"a.txt", "b.txt", "c.txt"} {
		r.ShowLiveToolContent(i, "write", file, "Poem\n")
		r.EndLiveToolContent()
		if !r.TryCompleteWriteResult(i, "⎿  "+file+" (5 bytes)") {
			t.Fatalf("completion for %s not placed", file)
		}
	}
	output := style.StripAnsi(out.String())
	for _, file := range []string{"a.txt", "b.txt"} {
		next := "b.txt"
		if file == "b.txt" {
			next = "c.txt"
		}
		segment := "⎿  " + file + " (5 bytes)\n\n✚ write " + next
		if !strings.Contains(output, segment) {
			t.Fatalf("early completion has no separator %q in %q", segment, output)
		}
		if strings.Contains(output, "⎿  "+file+" (5 bytes)\n\n\n✚ write "+next) {
			t.Fatalf("early completion has extra separator: %q", output)
		}
	}
}

func TestEarlyVerifiedEditThenWriteHasOneBlankSeparator(t *testing.T) {
	var out bytes.Buffer
	r := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "edit", "poem.txt", "+ improved\n")
	r.EndLiveToolContent()
	if !r.TryCompleteWriteResult(0, "⎿  poem.txt (+1 / -0 lines)") {
		t.Fatal("missing edit completion")
	}
	r.ShowLiveToolContent(1, "write", "new.txt", "New poem\n")
	output := style.StripAnsi(out.String())
	if !strings.Contains(output, "⎿  poem.txt (+1 / -0 lines)\n\n✚ write new.txt") {
		t.Fatalf("missing separator after edit: %q", output)
	}
}
