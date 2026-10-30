package stream

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func newWriteParser() *JSONStreamParser {
	p := NewJSONStreamParser(true)
	p.ActiveToolName = "write"
	p.ActiveToolIndex = 0
	return p
}

func TestWriteStreamBeforePathAndBeforeClosingQuote(t *testing.T) {
	p := newWriteParser()
	var out bytes.Buffer
	theme := style.GetTheme("plain")

	p.Feed(`{"content":"First`, &out, theme)
	if text := style.StripAnsi(out.String()); !strings.Contains(text, "✚ write\nFirst") {
		t.Fatalf("content should stream without path, newline or closing quote: %q", text)
	}
	if !p.ToolBodyStreamed[0] {
		t.Fatal("streamed write text must be tracked to suppress the duplicate execution preview")
	}
	p.Feed(` line","path":"poem.md"}`, &out, theme)
	got := style.StripAnsi(out.String())
	if !strings.Contains(got, "✚ write poem.md\nFirst line") {
		t.Fatalf("destination should update without repeating the content: %q", got)
	}
	if strings.Count(got, "✚ write") != 1 || strings.Count(got, "First line") != 1 {
		t.Fatalf("write header or body duplicated: %q", got)
	}
}

func TestWriteStreamWhenPathComesFirst(t *testing.T) {
	p := newWriteParser()
	var out bytes.Buffer
	theme := style.GetTheme("plain")
	p.Feed(`{"path":"poem.md","content":"Hi`, &out, theme)
	got := style.StripAnsi(out.String())
	if !strings.Contains(got, "✚ write poem.md\nHi") {
		t.Fatalf("write content should stream during the current chunk: %q", got)
	}
	p.Feed(` there\nsecond line"}`, &out, theme)
	got = style.StripAnsi(out.String())
	if !strings.Contains(got, "Hi there\nsecond line") {
		t.Fatalf("escaped newline should stream as content: %q", got)
	}
	if strings.Count(got, "✚ write") != 1 {
		t.Fatalf("duplicate title: %q", got)
	}
}

func TestOtherToolsKeepTargetGate(t *testing.T) {
	p := NewJSONStreamParser(true)
	p.ActiveToolName = "read"
	p.ActiveToolIndex = 0
	var out bytes.Buffer
	p.Feed(`{"content":"not displayed yet`, &out, style.GetTheme("plain"))
	if out.Len() != 0 {
		t.Fatalf("only write bypasses the path requirement: %q", out.String())
	}
}

func TestWriteEmptyContentDoesNotClaimStreamedBody(t *testing.T) {
	p := newWriteParser()
	var out bytes.Buffer
	p.Feed(`{"content":"","path":"poem.md"}`, &out, style.GetTheme("plain"))
	if p.ToolBodyStreamed[0] {
		t.Fatal("empty write body must not suppress the execution preview")
	}
}
