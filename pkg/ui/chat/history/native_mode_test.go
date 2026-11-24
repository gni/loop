package history

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func containsBraille(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28ff {
			return true
		}
	}
	return false
}

func TestDefaultConfigUsesNativeScroll(t *testing.T) {
	cfg := config.DefaultConfig()
	if !cfg.NativeScroll {
		t.Fatal("expected DefaultConfig().NativeScroll to be true")
	}
}

func TestNativeModeMouseDisabledOnNew(t *testing.T) {
	cfg := config.DefaultConfig()
	if !cfg.NativeScroll {
		t.Fatal("expected NativeScroll default true")
	}
	t.Cleanup(func() {
		terminal.Default().SetMouseEnabled(true)
	})

	var out bytes.Buffer
	_, height := ui.GetTerminalSize()
	a := &agent.Agent{Config: cfg}
	reader := &interceptor.KeyInterceptorReader{Agent: a, W: &out}
	pp := ui.NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)

	h, err := New(a, reader, pp, "native-mouse-test", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer h.Close()

	raw := out.String()
	for _, seq := range []string{"?1000h", "?1002h", "?1003h", "?1006h"} {
		if strings.Contains(raw, seq) {
			t.Fatalf("native mode must not emit mouse tracking sequence %q: %q", seq, raw)
		}
	}
}

func TestNativeModeStartupLogoAndFirstOutput(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.NativeScroll = true
	cfg.Logo = true
	cfg.ReducedMotion = true

	var out bytes.Buffer
	_, height := ui.GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, 0)
	partialMargin := fmt.Sprintf("\x1b[1;%dr", scrollBottom)

	a := &agent.Agent{Config: cfg}
	reader := &interceptor.KeyInterceptorReader{Agent: a, W: &out}
	reader.IsAtMainPrompt.Store(true)
	pp := ui.NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)

	h, err := New(a, reader, pp, "native-logo-test", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer h.Close()

	ui.TerminalMu.Lock()
	h.captureOnly = false
	h.logoEnabled = true
	h.SyncNativeScreenLocked(nil)
	logoVisible := h.logoVisible
	ui.TerminalMu.Unlock()

	if !logoVisible {
		t.Fatal("expected 3D startup logo to be visible on empty native screen")
	}
	logoFrame := out.String()
	if !containsBraille(logoFrame) {
		t.Fatalf("expected Braille logo runes in initial native frame, got %q", logoFrame)
	}
	if strings.Contains(logoFrame, partialMargin) {
		t.Fatalf("native logo paint must never emit partial DECSTBM %q", partialMargin)
	}

	out.Reset()
	pp.BeginScrollEntry()
	if gotLine := pp.GetPrintLine(); gotLine != 1 {
		t.Fatalf("expected Turn 1 native BeginScrollEntry to stay on row 1, got %d", gotLine)
	}
	if _, err := pp.Write([]byte("hello native world\n")); err != nil {
		t.Fatalf("pp.Write failed: %v", err)
	}

	afterWrite := out.String()
	if strings.Contains(afterWrite, partialMargin) {
		t.Fatalf("native first output must never emit partial DECSTBM %q", partialMargin)
	}
	if !strings.Contains(afterWrite, "hello native world") {
		t.Fatalf("expected native output to reach terminal, got %q", afterWrite)
	}

	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.logoVisible {
		t.Fatal("expected logo to clear on first native output")
	}
	if !h.nativeHasOutput {
		t.Fatal("expected nativeHasOutput to be true after output")
	}
	rows := h.buffer.View(h.buffer.Base())
	if !strings.Contains(style.StripAnsi(rows[0]), "hello native world") {
		t.Fatalf("expected shadow buffer row 0 to mirror output, got %q", rows[0])
	}
}

func TestNativeModeSlashMenuKeyboardAndRestore(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.NativeScroll = true

	var out bytes.Buffer
	width, height := ui.GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, 0)
	partialMargin := fmt.Sprintf("\x1b[1;%dr", scrollBottom)

	a := &agent.Agent{Config: cfg}
	reader := &interceptor.KeyInterceptorReader{Agent: a, W: &out}
	reader.IsAtMainPrompt.Store(true)
	pp := ui.NewPromptPreservingWriter(&out, height)
	pp.SetNativeScroll(true)

	h, err := New(a, reader, pp, "native-slash-test", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer h.Close()

	var transcript bytes.Buffer
	for i := range scrollBottom + 4 {
		fmt.Fprintf(&transcript, "chat line %02d\r\n", i)
	}

	// Emulate the full terminal screen (height rows) with rows 1..scrollBottom
	// holding the chat content and height-4 holding the stats/hint line.
	chatShadow := render.NewScrollback(width, scrollBottom)
	_, _ = chatShadow.Write(transcript.Bytes())
	beforeRows := chatShadow.View(chatShadow.Base())
	screen := render.NewScrollback(width, height)
	for r, line := range beforeRows {
		fmt.Fprintf(screen, "\x1b[%d;1H%s", r+1, line)
	}

	ui.TerminalMu.Lock()
	h.captureOnly = false
	h.SyncNativeScreenLocked(transcript.Bytes())
	ui.TerminalMu.Unlock()

	// Native scroll leaves PageUp and mouse sequences untouched.
	if got := string(h.FilterInput([]byte("\x1b[5~"))); got != "\x1b[5~" {
		t.Fatalf("expected PageUp passthrough in native mode, got %q", got)
	}
	if got := string(h.FilterInput([]byte("\x1b[<64;10;5M"))); got != "\x1b[<64;10;5M" {
		t.Fatalf("expected mouse sequence passthrough in native mode, got %q", got)
	}
	if h.RedrawLocked() {
		t.Fatal("expected RedrawLocked to return false in native mode")
	}

	// Open the keyboard slash-command palette.
	out.Reset()
	reader.SetPromptDraft("/co", 3)
	h.SyncSlashMenu("/co")
	if !h.SlashMenuActive() {
		t.Fatal("expected SlashMenuActive() to be true after SyncSlashMenu(\"/co\") in native mode")
	}
	ui.TerminalMu.Lock()
	h.PaintIfNeededLocked()
	ui.TerminalMu.Unlock()

	menuOut := out.String()
	if strings.Contains(menuOut, partialMargin) {
		t.Fatalf("native slash menu must never emit partial DECSTBM %q", partialMargin)
	}
	if !strings.Contains(menuOut, "/copy") {
		t.Fatalf("expected slash menu overlay to include /copy, got %q", menuOut)
	}
	_, _ = screen.Write([]byte(menuOut))

	// Arrow navigation moves highlight without partial scroll margins.
	out.Reset()
	if !h.StepSlashMenu(1) {
		t.Fatal("expected StepSlashMenu(1) to succeed in native mode")
	}
	stepOut := out.String()
	if strings.Contains(stepOut, partialMargin) {
		t.Fatalf("native StepSlashMenu must never emit partial DECSTBM %q", partialMargin)
	}
	_, _ = screen.Write([]byte(stepOut))

	// Escape dismisses the slash menu and restores every underlying chat row cleanly.
	out.Reset()
	if got := h.FilterInput([]byte("\x1b")); len(got) != 0 {
		t.Fatalf("expected Escape to be consumed by open slash menu, got %q", got)
	}
	if h.SlashMenuActive() {
		t.Fatal("expected slash menu to close on Escape")
	}
	restoreOut := out.String()
	if strings.Contains(restoreOut, partialMargin) {
		t.Fatalf("native slash menu restore must never emit partial DECSTBM %q", partialMargin)
	}
	_, _ = screen.Write([]byte(restoreOut))
	afterRows := screen.View(screen.Base())
	for i := range beforeRows {
		if strings.TrimRight(style.StripAnsi(afterRows[i]), " ") != strings.TrimRight(style.StripAnsi(beforeRows[i]), " ") {
			t.Fatalf("row %d mismatch after closing slash menu:\ngot:  %q\nwant: %q", i, afterRows[i], beforeRows[i])
		}
	}
	if height >= 6 {
		statsRow := height - 4 - 1
		if strings.TrimSpace(style.StripAnsi(afterRows[statsRow])) != "" {
			t.Fatalf("expected stats hint row to clear after closing slash menu, got %q", afterRows[statsRow])
		}
	}

	// Enter executes an exact/selected slash command cleanly.
	reader.SetPromptDraft("/copy", 5)
	h.SyncSlashMenu("/copy")
	if !h.SlashMenuActive() {
		t.Fatal("expected slash menu open for /copy")
	}
	_ = h.FilterInput([]byte("\r"))
	if h.SlashMenuActive() {
		t.Fatal("expected slash menu to close after Enter")
	}
}
