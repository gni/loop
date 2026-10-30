package ui

import (
	"bytes"
	"testing"

	"loop/pkg/ui/style"
)

func TestSubmittedPromptGapAccountsForExistingReplyNewlines(t *testing.T) {
	for _, chunks := range [][]string{
		{"Last reply."},
		{"Last reply.\n"},
		{"Last reply.\n\n"},
		{"Last reply.", "\n", "\n"},
		{"Last reply.\n", "\x1b[0m"},
	} {
		t.Run(chunks[0], func(t *testing.T) {
			useIsolatedCursorTestUI(t)
			var output bytes.Buffer
			writer := NewPromptPreservingWriter(&output, 24)
			for _, chunk := range chunks {
				if _, err := writer.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			writer.BeginScrollEntry()
			writer.EnsureActivityGap()
			if _, err := writer.Write([]byte("─── prompt\n> next prompt")); err != nil {
				t.Fatal(err)
			}
			// Two line feeds leave one empty row before the divider; the
			// divider's own newline advances to the submitted prompt text.
			if rows := writer.GetScrollCount(); rows != style.ActivityGapLines+2 {
				t.Fatalf("chunks=%q: advanced %d rows; want %d", chunks, rows, style.ActivityGapLines+2)
			}
		})
	}
}
