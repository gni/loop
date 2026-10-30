package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestExtraEditPreviewStreamsWithInlineResult(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "edit", "", "- old\n")
	if got := style.StripAnsi(b.String()); !strings.Contains(got, "✐ edit …\n- old") {
		t.Fatalf("edit must be visible before filename arrives: %q", got)
	}
	r.ShowLiveToolContent(0, "edit", "poem.go", "+ new\n")
	r.EndLiveToolContent()
	if !r.HasPendingWriteResult(0) {
		t.Fatal("streamed edit must reserve one verified result row")
	}
	if !r.DidStreamToolBody(0) || r.GetToolTitleLineNumber(0) < 0 {
		t.Fatal("edit lost its streamed title or contents")
	}
}

func TestExtraWriteAndEditPreviewsHaveOneBlankSeparator(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "write", "a.txt", "First\n")
	r.ShowLiveToolContent(1, "edit", "a.txt", "- First\n+ Second\n")
	r.ShowLiveToolContent(2, "write", "b.txt", "Third\n")
	r.EndLiveToolContent()
	out := style.StripAnsi(b.String())
	for _, part := range []string{"First\n\n\n✐ edit a.txt", "+ Second\n\n\n✚ write b.txt"} {
		if !strings.Contains(out, part) {
			t.Fatalf("missing blank separator %q in %q", part, out)
		}
	}
	if !r.HasPendingWriteResult(1) {
		t.Fatal("streamed edit must reserve a verified completion slot")
	}
}

func TestExtraAssistantProseSeparatedFromLiveEdit(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.Write("Fixing the typo now.")
	r.ShowLiveToolContent(0, "edit", "", "+ corrected\n")
	out := style.StripAnsi(b.String())
	if !strings.Contains(out, "Fixing the typo now.\n\n✐ edit …\n+ corrected") {
		t.Fatalf("assistant text and edit must be separated: %q", out)
	}
}

func TestExtraAssistantTextAfterLiveEditGetsItsOwnParagraph(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "edit", "a.go", "+ replacement\n")
	r.Write("Checking the updated file.")
	out := style.StripAnsi(b.String())
	if !strings.Contains(out, "+ replacement\n\n\nChecking the updated file.") {
		t.Fatalf("assistant text started inside the edit preview: %q", out)
	}
}

func TestExtraRegularToolHeadingSeparatedFromAssistantText(t *testing.T) {
	var b bytes.Buffer
	r := NewStreamRenderer(&b, style.GetTheme("plain"), false, true, "")
	r.Write("I will read that file.")
	r.StartToolCall("read", 0)
	r.SetToolTarget("a.go", 0)
	out := style.StripAnsi(b.String())
	if !strings.Contains(out, "I will read that file.\n\n") {
		t.Fatalf("missing separator before read call: %q", out)
	}
}
