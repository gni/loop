package terminal

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxOSC52PayloadBytes caps raw text encoded into an OSC 52 escape sequence so
// terminals with ~100 KB base64 limits do not drop or stall the sequence.
const MaxOSC52PayloadBytes = 74 * 1024

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// WriteOSC52 sends an OSC 52 clipboard write request to w.
// Uses standard OSC 52 for direct terminals and tmux (where set-clipboard handles
// standard OSC 52 directly without requiring allow-passthrough), and wraps DCS
// passthrough only for GNU screen.
func WriteOSC52(w io.Writer, content string) error {
	if w == nil || content == "" {
		return nil
	}
	payload := truncateUTF8Bytes(content, MaxOSC52PayloadBytes)
	if payload == "" {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	if os.Getenv("STY") != "" && os.Getenv("TMUX") == "" {
		_, err := fmt.Fprintf(w, "\x1bP\x1b]52;c;%s\x07\x1b\\", encoded)
		return err
	}
	_, err := fmt.Fprintf(w, "\x1b]52;c;%s\x07", encoded)
	return err
}

func desktopClipboardCandidates() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip.exe"}}
	default:
		var candidates [][]string
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			candidates = append(candidates, []string{"wl-copy"})
		}
		if os.Getenv("DISPLAY") != "" {
			candidates = append(candidates, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "-b", "-i"})
		}
		return candidates
	}
}

// HasDesktopClipboard reports whether a local desktop clipboard utility is
// configured and installed on this host (false on headless/SSH servers).
func HasDesktopClipboard() bool {
	for _, candidate := range desktopClipboardCandidates() {
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return true
		}
	}
	return false
}

// CopyToDesktopClipboard writes content directly to the local desktop clipboard
// utility when available (pbcopy, wl-copy, xclip, xsel, clip.exe).
func CopyToDesktopClipboard(content string) bool {
	if content == "" {
		return false
	}
	candidates := desktopClipboardCandidates()
	if len(candidates) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, candidate[1:]...)
		cmd.Stdin = strings.NewReader(content)
		cmd.WaitDelay = 250 * time.Millisecond
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}
