package history

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func selectionTestHistory(t *testing.T, lines int) (*ChatHistory, *bytes.Buffer) {
	t.Helper()
	state := ui.GetUI()
	state.StateMu.Lock()
	beforeApproval, beforeMenu := state.InApprovalPrompt, state.InInteractiveMenu
	state.InApprovalPrompt, state.InInteractiveMenu = false, false
	state.StateMu.Unlock()
	t.Cleanup(func() {
		state.StateMu.Lock()
		state.InApprovalPrompt, state.InInteractiveMenu = beforeApproval, beforeMenu
		state.StateMu.Unlock()
	})

	var output bytes.Buffer
	width, height := ui.GetTerminalSize()
	reader := &interceptor.KeyInterceptorReader{W: &output}
	reader.IsAtMainPrompt.Store(true)
	h := &ChatHistory{
		a:         &agent.Agent{Config: config.DefaultConfig()},
		reader:    reader,
		writer:    ui.NewPromptPreservingWriter(&output, height),
		searchRow: -1,
		buffer:    render.NewScrollback(width, style.ConsoleScrollBottom(height, 0)),
	}
	h.writer.SetInnerLocked(&historyOutput{destination: &output, history: h})
	t.Cleanup(h.Close)

	for i := range lines {
		fmt.Fprintf(h.buffer, "row %03d alpha beta gamma\n", i)
	}
	h.top = h.buffer.Base()
	return h, &output
}

func extractLastOSC52(raw string) (string, bool) {
	const prefix = "\x1b]52;c;"
	idx := strings.LastIndex(raw, prefix)
	if idx < 0 {
		return "", false
	}
	rest := raw[idx+len(prefix):]
	end := strings.IndexByte(rest, '\a')
	if end < 0 {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

func TestMouseDragSelectAndCopyOSC52(t *testing.T) {
	h, output := selectionTestHistory(t, 40)

	// Press at (col 1, row 2), drag to (col 13, row 2), release.
	if got := h.FilterInput([]byte("\x1b[<0;1;2M")); len(got) != 0 {
		t.Fatalf("press leaked: %q", got)
	}
	if got := h.FilterInput([]byte("\x1b[<32;13;2M")); len(got) != 0 {
		t.Fatalf("drag leaked: %q", got)
	}
	if got := h.FilterInput([]byte("\x1b[<0;13;2m")); len(got) != 0 {
		t.Fatalf("release leaked: %q", got)
	}

	ui.TerminalMu.Lock()
	rawOut := output.String()
	hasRange := h.sel.hasRange
	notice := h.sel.copiedNotice
	ui.TerminalMu.Unlock()

	if !hasRange {
		t.Fatal("expected selection highlight to remain visible after release")
	}
	if !strings.Contains(notice, "Copied") && !strings.Contains(notice, "Selected") {
		t.Fatalf("expected selection/copy toast notice, got %q", notice)
	}
	if !strings.Contains(notice, "Ctrl+X") {
		t.Fatalf("expected Ctrl+X hint in selection toast, got %q", notice)
	}
	copied, ok := extractLastOSC52(rawOut)
	if !ok {
		t.Fatalf("expected OSC 52 sequence in terminal output")
	}
	if !strings.HasPrefix(copied, "row ") || !strings.Contains(copied, "alpha") {
		t.Fatalf("unexpected copied text: %q", copied)
	}

	// Escape clears selection without leaking to prompt.
	if got := h.FilterInput([]byte("\x1b")); len(got) != 0 {
		t.Fatalf("Escape leaked while clearing selection: %q", got)
	}
	ui.TerminalMu.Lock()
	stillSelected := h.sel.hasRange
	ui.TerminalMu.Unlock()
	if stillSelected {
		t.Fatal("Escape did not clear active selection")
	}
}

func TestMouseSelectDragDownAutoScrollsHistory(t *testing.T) {
	h, output := selectionTestHistory(t, 120)

	// Scroll up into history so there are plenty of lines below the viewport.
	ui.TerminalMu.Lock()
	h.top = 10
	h.browsing = true
	bottomY := h.buffer.Height
	ui.TerminalMu.Unlock()

	// Start drag on row 3, then drag past the bottom edge of the chat viewport.
	h.FilterInput([]byte("\x1b[<0;1;3M"))
	h.FilterInput([]byte(fmt.Sprintf("\x1b[<32;20;%dM", bottomY+2)))

	// Let edge auto-scroll run for a few ticks.
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		ui.TerminalMu.Lock()
		topNow := h.top
		ui.TerminalMu.Unlock()
		if topNow >= 22 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	ui.TerminalMu.Lock()
	topAfterDrag := h.top
	focusRow := h.sel.focusRow
	anchorRow := h.sel.anchorRow
	ui.TerminalMu.Unlock()

	if topAfterDrag <= 10 {
		t.Fatalf("expected dragging past bottom edge to scroll down, top stayed at %d", topAfterDrag)
	}
	if focusRow <= anchorRow+5 {
		t.Fatalf("expected selection focusRow (%d) to extend well below anchorRow (%d)", focusRow, anchorRow)
	}

	// Release copies the entire multi-screen span.
	h.FilterInput([]byte(fmt.Sprintf("\x1b[<0;20;%dm", bottomY)))

	ui.TerminalMu.Lock()
	rawOut := output.String()
	active := h.sel.active
	ui.TerminalMu.Unlock()
	if active {
		t.Fatal("selection remained active after mouse release")
	}
	copied, ok := extractLastOSC52(rawOut)
	if !ok {
		t.Fatal("expected OSC 52 payload after auto-scroll selection")
	}
	if lines := strings.Count(copied, "\n") + 1; lines < 10 {
		t.Fatalf("expected multi-line auto-scrolled selection, got %d lines:\n%s", lines, copied)
	}
}

func TestMouseWheelWhileSelectingExtendsSelectionDown(t *testing.T) {
	h, output := selectionTestHistory(t, 100)

	ui.TerminalMu.Lock()
	h.top = 10
	h.browsing = true
	ui.TerminalMu.Unlock()

	// Press at row 3, then spin wheel DOWN twice (including terminals that emit
	// wheel release 65m and wheel+motion 97M while button 0 is held).
	h.FilterInput([]byte("\x1b[<0;1;3M"))
	h.FilterInput([]byte("\x1b[<65;15;3M"))
	h.FilterInput([]byte("\x1b[<65;15;3m"))
	h.FilterInput([]byte("\x1b[<97;15;3M"))
	h.FilterInput([]byte("\x1b[<97;15;3m"))

	ui.TerminalMu.Lock()
	topNow := h.top
	activeNow := h.sel.active
	anchorRow := h.sel.anchorRow
	focusRow := h.sel.focusRow
	ui.TerminalMu.Unlock()

	if !activeNow {
		t.Fatal("wheel release event prematurely terminated active drag selection")
	}
	if topNow != 16 {
		t.Fatalf("expected wheel down twice while selecting to scroll top 10->16, got %d", topNow)
	}
	if focusRow != anchorRow+6 {
		t.Fatalf("expected focusRow to advance 6 rows below anchorRow (%d), got %d", anchorRow, focusRow)
	}

	// Release copies all 7 spanned lines.
	h.FilterInput([]byte("\x1b[<0;15;3m"))
	ui.TerminalMu.Lock()
	rawOut := output.String()
	ui.TerminalMu.Unlock()
	copied, ok := extractLastOSC52(rawOut)
	if !ok {
		t.Fatal("expected OSC 52 copy on release")
	}
	if !strings.Contains(copied, "row 012") || !strings.Contains(copied, "row 018") {
		t.Fatalf("expected wheel-extended range row 012..018, got:\n%s", copied)
	}
}

func TestDoubleAndTripleClickSelection(t *testing.T) {
	h, output := selectionTestHistory(t, 30)

	// Double-click inside "alpha" (cols 9..13 -> 1-based x=10).
	h.FilterInput([]byte("\x1b[<0;10;2M"))
	h.FilterInput([]byte("\x1b[<0;10;2m"))
	h.FilterInput([]byte("\x1b[<0;10;2M"))
	h.FilterInput([]byte("\x1b[<0;10;2m"))

	ui.TerminalMu.Lock()
	rawOut := output.String()
	ui.TerminalMu.Unlock()
	copied, ok := extractLastOSC52(rawOut)
	if !ok || copied != "alpha" {
		t.Fatalf("expected double-click to select word %q, got ok=%v %q", "alpha", ok, copied)
	}

	// Third click immediately selects the full logical line.
	h.FilterInput([]byte("\x1b[<0;10;2M"))
	h.FilterInput([]byte("\x1b[<0;10;2m"))

	ui.TerminalMu.Lock()
	rawOut = output.String()
	ui.TerminalMu.Unlock()
	copied, ok = extractLastOSC52(rawOut)
	if !ok || !strings.HasSuffix(copied, "alpha beta gamma") {
		t.Fatalf("expected triple-click to select full line, got ok=%v %q", ok, copied)
	}
}

func TestSoftWrappedExtractionAndAnchorShift(t *testing.T) {
	sb := render.NewScrollback(12, 6)
	fmt.Fprint(sb, "abcdefghijklmnop\nsecond line\n")
	// Row 0 is "abcdefghijkl" (soft=true), row 1 is "mnop" (soft=false), row 2 is "second line".
	got := sb.ExtractPlainText(0, 0, 2, 11)
	want := "abcdefghijklmnop\nsecond line"
	if got != want {
		t.Fatalf("ExtractPlainText soft-wrap mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Highlighting preserves surrounding ANSI escapes and wide/combining graphemes.
	styled := "\x1b[32mθ̂ + ∑\x1b[0m end"
	hi := highlightStyledRow(styled, 0, 5, false, "\x1b[7m")
	if plain := style.StripAnsi(hi); plain != "θ̂ + ∑ end" {
		t.Fatalf("highlightStyledRow altered visible text: %q", plain)
	}
	if !strings.Contains(hi, "\x1b[7m") {
		t.Fatalf("expected highlight escape in %q", hi)
	}
}

func TestSelectionToastHonestRemoteVsLocalAndPreservedOnCtrlX(t *testing.T) {
	origCheck := hasDesktopClipboard
	t.Cleanup(func() { hasDesktopClipboard = origCheck })

	// Headless / SSH remote mode: must NOT falsely claim "Copied".
	hasDesktopClipboard = func() bool { return false }
	h, _ := selectionTestHistory(t, 20)
	h.FilterInput([]byte("\x1b[<0;1;2M"))
	h.FilterInput([]byte("\x1b[<32;15;4M"))
	h.FilterInput([]byte("\x1b[<0;15;4m"))

	ui.TerminalMu.Lock()
	remoteNotice := h.sel.copiedNotice
	ui.TerminalMu.Unlock()
	if strings.Contains(remoteNotice, "Copied") {
		t.Fatalf("headless remote toast must not claim Copied, got %q", remoteNotice)
	}
	if !strings.Contains(remoteNotice, "OSC 52 sent") || !strings.Contains(remoteNotice, "Ctrl+X: native copy") {
		t.Fatalf("unexpected headless remote toast: %q", remoteNotice)
	}

	// Pressing Ctrl+X (byte 24) must preserve the selection so Native Copy View receives it.
	if got := h.FilterInput([]byte{24}); len(got) != 1 || got[0] != 24 {
		t.Fatalf("Ctrl+X should pass through FilterInput, got %q", got)
	}
	selText := h.SelectedPlainText()
	if selText == "" || !strings.Contains(selText, "row ") {
		t.Fatalf("expected SelectedPlainText to remain intact after Ctrl+X, got %q", selText)
	}
	if transcript := h.FullRenderedTranscript(); !strings.Contains(transcript, "row 000") || !strings.Contains(transcript, "row 019") {
		t.Fatalf("expected FullRenderedTranscript to include all loaded rows, got %q", transcript)
	}

	// Local desktop clipboard mode: reports Copied plus Ctrl+X hint.
	hasDesktopClipboard = func() bool { return true }
	h.FilterInput([]byte("\x1b[<0;1;2M"))
	h.FilterInput([]byte("\x1b[<32;10;2M"))
	h.FilterInput([]byte("\x1b[<0;10;2m"))
	ui.TerminalMu.Lock()
	localNotice := h.sel.copiedNotice
	ui.TerminalMu.Unlock()
	if !strings.Contains(localNotice, "Copied") || !strings.Contains(localNotice, "Ctrl+X") {
		t.Fatalf("unexpected local clipboard toast: %q", localNotice)
	}
}

func TestNativeScrollModeBypassesChatHistory(t *testing.T) {
	h, output := selectionTestHistory(t, 30)
	h.a.Config.NativeScroll = true
	output.Reset()

	// FilterInput passes all keys (including mouse and PageUp) straight through.
	if got := h.FilterInput([]byte("\x1b[5~")); string(got) != "\x1b[5~" {
		t.Fatalf("expected PageUp to pass through in native_scroll mode, got %q", got)
	}
	if got := h.FilterInput([]byte("\x1b[<64;10;5M")); string(got) != "\x1b[<64;10;5M" {
		t.Fatalf("expected mouse sequence to pass through in native_scroll mode, got %q", got)
	}

	ui.TerminalMu.Lock()
	redrawn := h.RedrawLocked()
	h.paintLocked()
	ui.TerminalMu.Unlock()
	if redrawn {
		t.Fatal("expected RedrawLocked to return false in native_scroll mode")
	}
	if output.Len() != 0 {
		t.Fatalf("expected paintLocked to be a no-op in native_scroll mode, wrote %q", output.String())
	}
}
