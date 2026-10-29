package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestNumberedLiveEditRetainsColorsAndInlineSummary(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "edit", "", "")
	if got := style.StripAnsi(b.String()); !strings.Contains(got, "✎ edit …") {
		t.Fatalf("missing immediate edit heading: %q", got)
	}

	diff := "       ...\n4      original context\n\x1b[31m5    - old line\x1b[0m\n\x1b[32m5    + new line\x1b[0m\n"
	r.ShowLiveToolContent(0, "edit", "poem.txt", diff)
	r.EndLiveToolContent()
	if !r.HasPendingWriteResult(0) {
		t.Fatal("numbered edit diff must keep its completion position")
	}
	if !r.TryCompleteWriteResult(0, "⎿  poem.txt (+1 / -1 lines)") {
		t.Fatal("failed to render verified edit summary")
	}
	got := b.String()
	if !strings.Contains(got, "\x1b[31m5    - old line") || !strings.Contains(got, "\x1b[32m5    + new line") {
		t.Fatalf("diff colors stripped: %q", got)
	}
	plain := style.StripAnsi(got)
	if !strings.Contains(plain, "✎ edit poem.txt") || !strings.Contains(plain, "4      original context") || !strings.Contains(plain, "⎿  poem.txt (+1 / -1 lines)") {
		t.Fatalf("live numbered edit incomplete: %q", plain)
	}
	if strings.Count(plain, "5    + new line") != 1 {
		t.Fatalf("duplicate changed lines: %q", plain)
	}
}
