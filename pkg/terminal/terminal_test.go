package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalCursorAndReset(t *testing.T) {
	buf := &bytes.Buffer{}
	term := New(buf)

	term.RestoreCursor()
	if !strings.Contains(buf.String(), ShowCursor) {
		t.Fatalf("expected output to contain ShowCursor escape sequence, got %q", buf.String())
	}

	buf.Reset()
	term.ResetState()
	output := buf.String()
	if !strings.Contains(output, ExitAlternateScreen) {
		t.Errorf("expected reset to contain ExitAlternateScreen, got %q", output)
	}
	if !strings.Contains(output, DisableBracketedPaste) {
		t.Errorf("expected reset to contain DisableBracketedPaste, got %q", output)
	}
	if !strings.Contains(output, ResetStyles) {
		t.Errorf("expected reset to contain ResetStyles, got %q", output)
	}
}

func TestTerminalAlternateScreenRefCounting(t *testing.T) {
	buf := &bytes.Buffer{}
	term := New(buf)

	// First enter -> should emit EnterAlternateScreen
	term.EnterAltScreen()
	if !strings.Contains(buf.String(), EnterAlternateScreen) {
		t.Fatalf("expected EnterAlternateScreen on first enter, got %q", buf.String())
	}

	// Second enter -> should NOT re-emit EnterAlternateScreen
	buf.Reset()
	term.EnterAltScreen()
	if buf.Len() != 0 {
		t.Fatalf("expected no output on nested enter, got %q", buf.String())
	}

	// First exit -> should NOT emit ExitAlternateScreen because depth is still 1
	term.ExitAltScreen()
	if buf.Len() != 0 {
		t.Fatalf("expected no output on partial exit, got %q", buf.String())
	}

	// Second exit -> depth reaches 0, should emit ExitAlternateScreen
	term.ExitAltScreen()
	if !strings.Contains(buf.String(), ExitAlternateScreen) {
		t.Fatalf("expected ExitAlternateScreen on final exit, got %q", buf.String())
	}
}

func TestTerminalForceExitAltScreen(t *testing.T) {
	buf := &bytes.Buffer{}
	term := New(buf)

	term.EnterAltScreen()
	term.EnterAltScreen()
	buf.Reset()

	term.ForceExitAltScreen()
	if !strings.Contains(buf.String(), ExitAlternateScreen) {
		t.Fatalf("expected ExitAlternateScreen on force exit, got %q", buf.String())
	}

	// Another force exit should produce no output since depth is 0
	buf.Reset()
	term.ForceExitAltScreen()
	if buf.Len() != 0 {
		t.Fatalf("expected no output on second force exit, got %q", buf.String())
	}
}

func TestTerminalRecoverPanic(t *testing.T) {
	buf := &bytes.Buffer{}
	term := New(buf)

	cleanedUp := false
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic to be re-thrown")
		}
		if !cleanedUp {
			t.Fatal("expected cleanup callback to be called before re-panic")
		}
		if !strings.Contains(buf.String(), ExitAlternateScreen) {
			t.Fatalf("expected terminal recovery sequence in output, got %q", buf.String())
		}
	}()

	func() {
		defer term.RecoverPanic(func() {
			cleanedUp = true
		})
		panic("test panic")
	}()
}
