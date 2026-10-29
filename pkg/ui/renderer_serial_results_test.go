package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestSequentialPreviewNeverLeavesPlaceholder(t *testing.T) {
	var output bytes.Buffer
	r := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	r.SetSequentialToolResults(true)
	r.StartToolCall("write", 0)
	r.WriteToolCall(`{"path":"a.txt","content":"hello"}`)
	r.Flush()
	if strings.Contains(output.String(), "writing...") {
		t.Fatalf("stale in-progress row: %q", output.String())
	}
	if r.HasPendingWriteResult(0) {
		t.Fatal("sequential result unexpectedly reserved a terminal row")
	}
}
