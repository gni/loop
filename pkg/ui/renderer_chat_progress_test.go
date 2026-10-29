package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestDeferredToolProgressAppearsInChatTranscript(t *testing.T) {
	var output bytes.Buffer
	sr := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	sr.SetSequentialToolResults(true)
	sr.StartToolCall("write", 0)
	sr.WriteToolCall(`{"content":"First poem","path":"first.md"}`)

	sr.ShowToolDraft(1, "write", "Second poem", 40)
	if !strings.Contains(style.StripAnsi(output.String()), "↳ receiving write #2: Second poem") {
		t.Fatalf("progress not rendered in chat: %q", output.String())
	}
	sr.ShowToolDraft(1, "write", "Second poem grows", 70)
	plain := style.StripAnsi(output.String())
	if strings.Count(plain, "↳ receiving write #2") != 1 || !strings.Contains(plain, "Second poem grows") {
		t.Fatalf("progress was duplicated rather than updated: %q", plain)
	}

	sr.ClearToolDraft()
	if !sr.HasPendingWriteResult(0) {
		t.Fatal("progress row was not assigned to the first write's result")
	}
	if !sr.TryCompleteWriteResult(0, "⎿  first.md (10 bytes)") {
		t.Fatal("could not replace draft with first write result")
	}
	plain = style.StripAnsi(output.String())
	if strings.Contains(plain, "receiving write") || strings.Contains(plain, "writing...") || strings.Count(plain, "⎿  first.md") != 1 {
		t.Fatalf("transient progress remained in chat: %q", plain)
	}
	if !strings.Contains(plain, "First poem\n⎿  first.md (10 bytes)") {
		t.Fatalf("result not paired with first preview: %q", plain)
	}
}

func TestDeferredToolProgressSanitizesControlCharacters(t *testing.T) {
	var output bytes.Buffer
	sr := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	sr.SetSequentialToolResults(true)
	sr.StartToolCall("write", 0)
	sr.WriteToolCall(`{"path":"first.md","content":"First"}`)
	sr.ShowToolDraft(1, "write", "text\x1b[31m\nmore", 16)
	plain := output.String()
	if strings.Contains(plain, "[31m") || strings.Contains(plain, "\nmore") {
		t.Fatalf("untrusted fragments contained terminal controls: %q", plain)
	}
	sr.ClearToolDraft()
}

func TestNoChatPreviewWhenWriterCannotRedraw(t *testing.T) {
	var output strings.Builder
	sr := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	sr.SetSequentialToolResults(true)
	sr.ShowToolDraft(1, "write", "data", 4)
	if output.Len() != 0 {
		t.Fatalf("non-interactive writer received an unerasable draft: %q", output.String())
	}
}

func TestUnnamedToolProgressIsClearedBeforeFirstTitle(t *testing.T) {
	var output bytes.Buffer
	sr := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	sr.SetSequentialToolResults(true)
	sr.ShowToolDraft(3, "", "", 28)
	if !strings.Contains(output.String(), "receiving tool #4") {
		t.Fatalf("early unnamed tool showed no progress: %q", output.String())
	}
	sr.StartToolCall("write", 0)
	plain := style.StripAnsi(output.String())
	if strings.Contains(plain, "receiving tool #4") || strings.Count(plain, "◆ write") != 1 {
		t.Fatalf("unnamed preview was not cleared: %q", plain)
	}
}

func TestIncompleteWriteDoesNotReserveCompletionRow(t *testing.T) {
	var output bytes.Buffer
	sr := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	sr.SetSequentialToolResults(true)
	sr.StartToolCall("write", 0)
	sr.ShowToolDraft(1, "write", "second", 6)
	sr.ClearToolDraft()
	if sr.HasPendingWriteResult(0) {
		t.Fatal("incomplete write reserved a stale completion row")
	}
}
