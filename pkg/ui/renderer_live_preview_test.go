package ui

import (
	"bytes"
	"loop/pkg/ui/style"
	"strings"
	"testing"
)

func TestLivePreviewShowsBodyAndUpdatesTitleInChat(t *testing.T) {
	var out bytes.Buffer
	sr := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	sr.ShowToolLivePreview(0, "write", "First line\nSecond", 30)
	plain := style.StripAnsi(out.String())
	if !strings.Contains(plain, "◆ write …") || !strings.Contains(plain, "First line\nSecond") {
		t.Fatalf("not showing live multiline content: %q", plain)
	}
	sr.ShowToolLivePreview(0, "write poem.md", "First line\nSecond done", 60)
	plain = style.StripAnsi(out.String())
	if strings.Contains(plain, "◆ write …") || !strings.Contains(plain, "◆ write poem.md") || !strings.Contains(plain, "Second done") {
		t.Fatalf("did not redraw title/content in-place: %q", plain)
	}
	sr.ClearToolLivePreview()
	if strings.Contains(out.String(), "First line") || strings.Contains(out.String(), "◆ write") {
		t.Fatalf("left stale draft: %q", out.String())
	}
}

func TestLivePreviewDoesNotPrintControlSequencesOrDuplicateOnPipe(t *testing.T) {
	var out strings.Builder
	sr := NewStreamRenderer(&out, style.GetTheme("plain"), false, true, "")
	sr.ShowToolLivePreview(1, "write", "hello\x1b[2J\rworld", 23)
	if out.Len() != 0 {
		t.Fatalf("cannot safely repaint pipe: %q", out.String())
	}
	var terminal bytes.Buffer
	sr = NewStreamRenderer(&terminal, style.GetTheme("plain"), false, true, "")
	sr.ShowToolLivePreview(1, "write", "hello\x1b[2J\rworld", 23)
	if strings.Contains(terminal.String(), "[2J") {
		t.Fatalf("raw control escaped: %q", terminal.String())
	}
}
