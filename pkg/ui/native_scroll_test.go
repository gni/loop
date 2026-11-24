package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func TestPromptPreservingWriterNativeScrollEmitsFullScreenScroll(t *testing.T) {
	_, height := GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, 0)

	var out bytes.Buffer
	pp := NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)

	footerRedraws := 0
	pp.SetFooterRedrawHook(func(w io.Writer) {
		footerRedraws++
		_, _ = io.WriteString(w, "FOOTER_REDRAWN")
	})

	// Write enough lines to scroll past scrollBottom.
	pp.SetPrintLine(scrollBottom)
	if _, err := pp.Write([]byte("line-one\nline-two\n")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, fmt.Sprintf("\x1b[1;%dr", scrollBottom)) {
		t.Fatalf("native_scroll must never emit partial DECSTBM \\x1b[1;%dr: %q", scrollBottom, raw)
	}
	if !strings.Contains(raw, terminal.ResetScrollRegion) {
		t.Fatalf("expected full-screen scroll reset \\x1b[r in output: %q", raw)
	}
	clearFooter := fmt.Sprintf("\x1b[%d;1H\x1b[J", scrollBottom+1)
	scrollAtBottom := fmt.Sprintf("\x1b[%d;1H\r\n", height)
	if !strings.Contains(raw, clearFooter) {
		t.Fatalf("expected footer clear sequence %q before scrolling: %q", clearFooter, raw)
	}
	if !strings.Contains(raw, scrollAtBottom) {
		t.Fatalf("expected full-screen scroll %q at terminal bottom: %q", scrollAtBottom, raw)
	}
	if strings.Index(raw, clearFooter) > strings.Index(raw, scrollAtBottom) {
		t.Fatalf("footer must be cleared BEFORE scrolling at row %d", height)
	}
	if footerRedraws != 1 || !strings.Contains(raw, "FOOTER_REDRAWN") {
		t.Fatalf("expected footer hook to redraw once inside sync frame, got count=%d", footerRedraws)
	}
	if strings.Index(raw, "FOOTER_REDRAWN") < strings.Index(raw, scrollAtBottom) {
		t.Fatalf("footer must be redrawn AFTER scrolling")
	}
}

func TestResetAfterNativeReplayLockedTracksOverflowRows(t *testing.T) {
	_, height := GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, 0)
	pp := NewPromptPreservingWriter(io.Discard, height)

	shortTranscript := []byte("line 1\r\nline 2\r\nline 3")
	if endRow := pp.ResetAfterNativeReplayLocked(height, shortTranscript); endRow != 3 {
		t.Fatalf("short transcript endRow = %d, want 3", endRow)
	}
	if pp.PrintLineLocked() != 3 {
		t.Fatalf("short transcript printLine = %d, want 3", pp.PrintLineLocked())
	}

	var longBuf bytes.Buffer
	for i := range height + 10 {
		if i > 0 {
			longBuf.WriteString("\r\n")
		}
		fmt.Fprintf(&longBuf, "transcript line %d", i)
	}
	if endRow := pp.ResetAfterNativeReplayLocked(height, longBuf.Bytes()); endRow != height {
		t.Fatalf("long transcript endRow = %d, want %d", endRow, height)
	}
	if pp.PrintLineLocked() != scrollBottom {
		t.Fatalf("long transcript printLine = %d, want clamped scrollBottom %d", pp.PrintLineLocked(), scrollBottom)
	}
}

func TestRedrawScreenWithNoticeNativeScrollUsesFullScreenMargins(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.NativeScroll = true
	theme := style.GetTheme("studio")
	uiImpl := NewAgentUI(cfg, theme)
	t.Cleanup(func() {
		uiImpl.SetConfig(config.DefaultConfig())
	})
	_, height := GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, 0)

	var out bytes.Buffer
	pp := NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)
	uiImpl.PPWriter = pp
	uiImpl.Enabled = true

	var msgs []db.Message
	for i := range height {
		msgs = append(msgs, db.Message{Role: "user", Content: fmt.Sprintf("question %d", i)})
	}
	a := &agent.Agent{Config: cfg, UI: uiImpl}
	ki := &interceptor.KeyInterceptorReader{Agent: a, Theme: theme, W: &out, Messages: &msgs}

	RedrawScreenWithNotice(&out, a, ki, "")
	raw := out.String()
	if strings.Contains(raw, fmt.Sprintf("\x1b[1;%dr", scrollBottom)) {
		t.Fatalf("native_scroll RedrawScreen must never set partial scroll region \\x1b[1;%dr", scrollBottom)
	}
	if !strings.Contains(raw, "\x1b[r\x1b[2J\x1b[H") {
		t.Fatalf("expected full-screen reset and clear in native_scroll RedrawScreen")
	}
	if !strings.Contains(raw, fmt.Sprintf("\x1b[%d;1H\r\n", height)) {
		t.Fatalf("expected trailing scroll padding at row %d so last lines sit above footer", height)
	}
}

func TestBeginScrollEntryNativeScrollStartsAtTop(t *testing.T) {
	_, height := GetTerminalSize()
	var out bytes.Buffer
	pp := NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)

	if got := pp.GetPrintLine(); got != 1 {
		t.Fatalf("fresh native_scroll writer printLine = %d, want 1", got)
	}
	pp.BeginScrollEntry()
	if got := pp.GetPrintLine(); got != 1 {
		t.Fatalf("BeginScrollEntry on empty native_scroll screen jumped printLine to %d, want 1", got)
	}
}
