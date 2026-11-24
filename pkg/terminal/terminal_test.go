package terminal

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSetMouseEnabledSuppressesTrackingAcrossModes(t *testing.T) {
	var buf bytes.Buffer
	term := New(&buf)

	// Disabling before BeginMouseTracking must suppress enable sequences.
	term.SetMouseEnabled(false)
	stop := term.BeginMouseTracking(&buf)
	defer stop()
	if buf.Len() != 0 {
		t.Fatalf("expected no mouse tracking output while disabled, got %q", buf.String())
	}

	// SetMouseMotion, SuspendMouseTracking, and Enter/ExitAltScreen must keep mouse off.
	term.SetMouseMotion(true)
	if strings.Contains(buf.String(), "?1000h") || strings.Contains(buf.String(), "?1003h") {
		t.Fatalf("SetMouseMotion enabled mouse while disabled: %q", buf.String())
	}
	resume := term.SuspendMouseTracking()
	resume()
	if strings.Contains(buf.String(), "?1000h") || strings.Contains(buf.String(), "?1002h") || strings.Contains(buf.String(), "?1003h") {
		t.Fatalf("SuspendMouseTracking resume enabled mouse while disabled: %q", buf.String())
	}
	term.EnterAltScreen(&buf)
	buf.Reset()
	term.ExitAltScreen(&buf)
	if strings.Contains(buf.String(), "?1000h") || strings.Contains(buf.String(), "?1002h") || strings.Contains(buf.String(), "?1003h") {
		t.Fatalf("ExitAltScreen enabled mouse while disabled: %q", buf.String())
	}

	// Re-enabling immediately writes the active mouse enable sequence.
	buf.Reset()
	term.SetMouseEnabled(true)
	if !strings.Contains(buf.String(), EnableMouseMotionTracking) {
		t.Fatalf("expected EnableMouseMotionTracking after re-enabling mouse, got %q", buf.String())
	}

	// Disabling again immediately writes DisableMouseTracking.
	buf.Reset()
	term.SetMouseEnabled(false)
	if !strings.Contains(buf.String(), DisableMouseTracking) {
		t.Fatalf("expected DisableMouseTracking when disabling active mouse, got %q", buf.String())
	}
}

func TestWriteOSC52DirectTmuxScreenAndUTF8Truncation(t *testing.T) {
	t.Run("direct terminal", func(t *testing.T) {
		t.Setenv("TMUX", "")
		t.Setenv("STY", "")
		var buf bytes.Buffer
		if err := WriteOSC52(&buf, "hello world"); err != nil {
			t.Fatalf("WriteOSC52 failed: %v", err)
		}
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello world")) + "\x07"
		if buf.String() != want {
			t.Fatalf("got %q, want %q", buf.String(), want)
		}
	})

	t.Run("tmux sends standard OSC 52 without passthrough wrapper", func(t *testing.T) {
		t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
		t.Setenv("STY", "")
		var buf bytes.Buffer
		if err := WriteOSC52(&buf, "tmux payload"); err != nil {
			t.Fatalf("WriteOSC52 failed: %v", err)
		}
		if strings.Contains(buf.String(), "\x1bPtmux;") {
			t.Fatalf("must not wrap in DCS tmux passthrough (blocked by default tmux config): %q", buf.String())
		}
		want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("tmux payload")) + "\x07"
		if buf.String() != want {
			t.Fatalf("got %q, want %q", buf.String(), want)
		}
	})

	t.Run("GNU screen wraps DCS passthrough", func(t *testing.T) {
		t.Setenv("TMUX", "")
		t.Setenv("STY", "1234.pts-0.host")
		var buf bytes.Buffer
		if err := WriteOSC52(&buf, "screen payload"); err != nil {
			t.Fatalf("WriteOSC52 failed: %v", err)
		}
		want := "\x1bP\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("screen payload")) + "\x07\x1b\\"
		if buf.String() != want {
			t.Fatalf("got %q, want %q", buf.String(), want)
		}
	})

	t.Run("UTF-8 safe truncation at MaxOSC52PayloadBytes", func(t *testing.T) {
		t.Setenv("TMUX", "")
		t.Setenv("STY", "")
		// Repeat a 3-byte UTF-8 rune ("∑") so MaxOSC52PayloadBytes lands mid-rune.
		big := strings.Repeat("∑", MaxOSC52PayloadBytes/3+100)
		var buf bytes.Buffer
		if err := WriteOSC52(&buf, big); err != nil {
			t.Fatalf("WriteOSC52 failed: %v", err)
		}
		raw := buf.String()
		b64 := strings.TrimSuffix(strings.TrimPrefix(raw, "\x1b]52;c;"), "\x07")
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("invalid base64: %v", err)
		}
		if len(decoded) > MaxOSC52PayloadBytes {
			t.Fatalf("decoded length %d exceeds MaxOSC52PayloadBytes %d", len(decoded), MaxOSC52PayloadBytes)
		}
		if !utf8.Valid(decoded) {
			t.Fatalf("truncated OSC 52 payload split a UTF-8 rune")
		}
	})
}
