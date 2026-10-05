package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func useIsolatedCursorTestUI(t *testing.T) {
	t.Helper()

	previous := ActiveUI
	ActiveUI = &AgentUIImpl{}
	t.Cleanup(func() {
		ActiveUI = previous
	})
}

func TestPromptPreservingWriterKeepsCursorModeStableWhileStreaming(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 30)
	writer.SetPromptCol(5)
	output.Reset()

	if _, err := writer.Write([]byte("token")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := output.String()
	if !strings.Contains(got, "\x1b8") {
		t.Fatalf("Write() did not restore the cursor to the prompt position: %q", got)
	}
	if strings.Contains(got, "\x1b[?25h") {
		t.Fatalf("Write() should not re-emit ?25h on every token chunk (causes fast strobe blink): %q", got)
	}
}

func TestRedrawTypeAheadLeavesCursorAtLiveInputPosition(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 24)
	a := &agent.Agent{
		Config:        &config.Config{},
		CurrentWriter: writer,
	}
	reader := &keyInterceptorReader{
		agent:           a,
		w:               &output,
		typeAheadBuffer: []byte("hello"),
	}

	reader.redrawTypeAhead()

	got := output.String()
	if strings.Contains(got, "\x1b[?25") {
		t.Fatalf("redrawTypeAhead() changed cursor visibility: %q", got)
	}
	if strings.Contains(got, "\x1b7") || strings.Contains(got, "\x1b8") {
		t.Fatalf("redrawTypeAhead() restored the cursor away from live input: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[22;8H") {
		t.Fatalf("redrawTypeAhead() did not leave the cursor after the input: %q", got)
	}
	if writer.promptCol != 8 {
		t.Fatalf("prompt column = %d; want 8", writer.promptCol)
	}
}

func TestBackgroundUIUpdatesNeverResetCursorBlink(t *testing.T) {
	useIsolatedCursorTestUI(t)

	var output bytes.Buffer
	theme := UITheme{}

	// 1. DrawStaticStatsLineLocked
	output.Reset()
	DrawStaticStatsLineLocked(&output, theme, "• · ·", "")
	got := output.String()
	if strings.Contains(got, "\x1b[?25h") || strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("DrawStaticStatsLineLocked emitted cursor visibility codes (?25h/?25l) which reset terminal blink: %q", got)
	}
	if !strings.Contains(got, "\x1b7") || !strings.Contains(got, "\x1b8") {
		t.Fatalf("DrawStaticStatsLineLocked did not save/restore cursor: %q", got)
	}

	// 2. DrawStatusBarLocked
	output.Reset()
	getUI().Enabled = true
	getUI().LastStatusBarText = "" // ensure it renders
	DrawStatusBarLocked(&output, theme)
	got = output.String()
	if strings.Contains(got, "\x1b[?25h") || strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("DrawStatusBarLocked emitted cursor visibility codes (?25h/?25l) which reset terminal blink: %q", got)
	}
	if !strings.Contains(got, "\x1b7") || !strings.Contains(got, "\x1b8") {
		t.Fatalf("DrawStatusBarLocked did not save/restore cursor: %q", got)
	}

	// 3. ReplaceScrollBlockBack
	output.Reset()
	writer := NewPromptPreservingWriter(&output, 30)
	writer.SetPrintLine(10)
	output.Reset()
	if ok := writer.ReplaceScrollBlockBack(2, []string{"updated line"}); !ok {
		t.Fatalf("ReplaceScrollBlockBack failed")
	}
	got = output.String()
	if strings.Contains(got, "\x1b[?25h") || strings.Contains(got, "\x1b[?25l") {
		t.Fatalf("ReplaceScrollBlockBack emitted cursor visibility codes (?25h/?25l): %q", got)
	}
	if !strings.Contains(got, "\x1b7") || !strings.Contains(got, "\x1b8") {
		t.Fatalf("ReplaceScrollBlockBack did not save/restore cursor: %q", got)
	}
}

func TestGetTerminalLine(t *testing.T) {
	var in, out bytes.Buffer
	rl := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{&in, &out}, "")

	line, pos := getTerminalLine(rl)
	if line != "" || pos != 0 {
		t.Fatalf("expected empty line, got %q, %d", line, pos)
	}
}

func TestDrawConsoleStaticControlsCursorPosition(t *testing.T) {
	useIsolatedCursorTestUI(t)
	a := &agent.Agent{Config: &config.Config{}}
	ki := &keyInterceptorReader{
		agent:            a,
		currentInputLine: "hello",
		currentInputPos:  5,
	}

	var buf bytes.Buffer
	drawConsoleStaticControlsLocked(&buf, a, ki, nil, true)
	out := buf.String()

	// Prompt row should be 22 in 24-height terminal
	// Prefix is "> " (len 2)
	// Cursor col should be 1 + 2 + 5 = 8
	if !strings.Contains(out, "\x1b[22;8H") {
		t.Fatalf("expected cursor at row 22, col 8, got:\n%q", out)
	}

	// Must begin with cursor hidden (\x1b[?25l) to prevent flickering on column 1 ('>')
	if !strings.HasPrefix(out, "\x1b[?25l") {
		t.Fatalf("expected output to start with cursor hidden, got:\n%q", out)
	}

	// Must show cursor (\x1b[?25h) only at final cursor position
	if !strings.Contains(out, "\x1b[22;8H\x1b[?25h") {
		t.Fatalf("expected cursor to be revealed at final position (row 22, col 8), got:\n%q", out)
	}
}
