package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReplaceScrollBlockBackDoesNotLeaveLastMenuOptionBehind(t *testing.T) {
	var out bytes.Buffer
	writer := NewPromptPreservingWriter(&out, 30)
	if _, err := writer.Write([]byte("    Other: write what you want\n")); err != nil {
		t.Fatal(err)
	}
	if !writer.pendingLineFeed {
		t.Fatal("expected the trailing newline to be deferred at the scroll boundary")
	}

	options := []string{
		"    New file: static.md",
		"    Overwrite poem.md",
		"  > Append to poem.md",
		"    Other: write what you want",
	}
	out.Reset()
	if !writer.ReplaceScrollBlockBack(len(options), options) {
		t.Fatal("menu repaint failed")
	}
	start := writer.getScrollBottom() - len(options) + 1
	for i, option := range options {
		want := fmt.Sprintf("\x1b[%d;1H\x1b[2K%s", start+i, option)
		if !strings.Contains(out.String(), want) {
			t.Fatalf("menu row %d not repainted in place; missing %q in %q", i, want, out.String())
		}
	}
}
