package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestLiveEditCompletionReplacesOnlyItsResultRow(t *testing.T) {
	var output bytes.Buffer
	r := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	r.ShowLiveToolContent(0, "edit", "", "- old line\n")
	r.ShowLiveToolContent(0, "edit", "poem.txt", "+ new line\n")
	r.EndLiveToolContent()

	if !r.HasPendingWriteResult(0) {
		t.Fatal("streamed edit has no result row")
	}
	if !r.TryCompleteWriteResult(0, "⎿  poem.txt (+1 / -1 lines)") {
		t.Fatal("verified edit result did not replace its reserved row")
	}
	if r.HasPendingWriteResult(0) {
		t.Fatal("completed result row remains pending")
	}
	shown := style.StripAnsi(output.String())
	for _, required := range []string{"- old line", "+ new line", "⎿  poem.txt (+1 / -1 lines)"} {
		if !strings.Contains(shown, required) {
			t.Fatalf("missing %q in %q", required, shown)
		}
	}
	r.MarkPendingWriteResults("Not executed")
	if strings.Contains(output.String(), "Not executed") {
		t.Fatal("completed edit was later labelled unexecuted")
	}
}

type unavailableResultRowWriter struct{ bytes.Buffer }

func (*unavailableResultRowWriter) ReplaceScrollLineBack(int, string) bool { return false }

func TestOffscreenLiveResultDoesNotRetainCompletionSlot(t *testing.T) {
	for _, toolName := range []string{"write", "edit"} {
		t.Run(toolName, func(t *testing.T) {
			output := &unavailableResultRowWriter{}
			r := NewStreamRenderer(output, style.GetTheme("plain"), false, true, "")
			r.ShowLiveToolContent(0, toolName, "poem.txt", "generated text\n")
			r.EndLiveToolContent()
			if !r.HasPendingWriteResult(0) {
				t.Fatal("missing reserved result row")
			}
			if r.TryCompleteWriteResult(0, "⎿  poem.txt (saved)") {
				t.Fatal("offscreen result row reported successful replacement")
			}
			if r.HasPendingWriteResult(0) {
				t.Fatal("offscreen completion was not retired")
			}
			r.MarkPendingWriteResults("Not executed")
			if strings.Contains(output.String(), "Not executed") {
				t.Fatal("finished operation was labelled unexecuted")
			}
		})
	}
}
