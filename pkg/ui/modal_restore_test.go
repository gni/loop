package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func TestRestoreAfterModalDoesNotMoveChatToTop(t *testing.T) {
	useIsolatedCursorTestUI(t)

	previous := modalHadTTY
	modalHadTTY = func() bool { return true }
	t.Cleanup(func() { modalHadTTY = previous })

	a := &agent.Agent{Config: &config.Config{}}
	reader := &keyInterceptorReader{Agent: a}

	uiImpl := getUI()
	uiImpl.Enabled = true

	var output bytes.Buffer
	RestoreAfterModal(&output, a, reader, nil)

	raw := output.String()
	// The conversation is still intact in the primary buffer because the modal drew on
	// the alternate screen. Any clear or absolute cursor jump to row 1 is what pushes the
	// chat to the top of the screen.
	for _, destructive := range []string{"\x1b[2J", "\x1b[H\x1b[J", "\x1b[r\x1b[H", "\x1b[1;1H"} {
		if strings.Contains(raw, destructive) {
			t.Fatalf("modal restore erased the conversation region with %q: %q", destructive, raw)
		}
	}
	if !strings.Contains(raw, "─── prompt ") {
		t.Fatalf("modal restore did not repaint the fixed prompt controls: %q", raw)
	}

	// The status bar is delta-cached and only draws when enabled. With an unchanged state
	// it would be skipped, so the cache must have been invalidated and the bar emitted.
	if uiImpl.LastStatusBarText == "" {
		t.Fatalf("status bar was skipped by the delta cache: %q", raw)
	}
	if !strings.Contains(raw, "▾") {
		t.Fatalf("status bar separator line missing from modal restore output: %q", raw)
	}
}

// Without a terminal the modal could not use the alternate screen, so its output landed
// on the primary buffer and a full repaint is the correct response.
func TestRestoreAfterModalFallsBackToFullRepaintWithoutTTY(t *testing.T) {
	useIsolatedCursorTestUI(t)

	previous := modalHadTTY
	modalHadTTY = func() bool { return false }
	t.Cleanup(func() { modalHadTTY = previous })

	a := &agent.Agent{Config: &config.Config{}}
	reader := &keyInterceptorReader{Agent: a}

	var output bytes.Buffer
	RestoreAfterModal(&output, a, reader, nil)

	raw := output.String()
	if !strings.Contains(raw, "\x1b[H\x1b[J") {
		t.Fatalf("non-tty modal restore must repaint the whole screen: %q", raw)
	}
}
