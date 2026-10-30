package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestWritePreviewWithoutDestinationSeparatesToolResult(t *testing.T) {
	var out bytes.Buffer
	theme := style.GetTheme("plain")
	renderer := NewStreamRenderer(&out, theme, false, true, "")
	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(`{"content":"Partial`)
	if text := style.StripAnsi(out.String()); !strings.Contains(text, "✚ write\nPartial") {
		t.Fatalf("preview did not stream immediately: %q", text)
	}
	renderer.WriteToolCall(` line","path":"poem.md"}`)
	renderer.Flush()
	got := style.StripAnsi(out.String())
	if !strings.Contains(got, "Partial line\n") {
		t.Fatalf("unterminated preview must end before result: %q", got)
	}
	if strings.Count(got, "✚ write") != 1 || strings.Count(got, "Partial line") != 1 {
		t.Fatalf("header or body duplicated: %q", got)
	}
}
