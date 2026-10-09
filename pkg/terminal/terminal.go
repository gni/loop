package terminal

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

const (
	// ShowCursor sequence makes the text cursor visible.
	ShowCursor = "\x1b[?25h"
	// HideCursor sequence makes the text cursor hidden.
	HideCursor = "\x1b[?25l"
	// EnableBracketedPaste sequence enables bracketed paste mode.
	EnableBracketedPaste = "\x1b[?2004h"
	// DisableBracketedPaste sequence disables bracketed paste mode.
	DisableBracketedPaste = "\x1b[?2004l"
	// EnterAlternateScreen sequence switches terminal to alternate screen buffer.
	EnterAlternateScreen = "\x1b[?1049h\x1b[r\x1b[2J\x1b[H"
	// ExitAlternateScreen sequence restores primary screen buffer and shows cursor.
	ExitAlternateScreen = "\x1b[?1049l\x1b[?25h"
	// ResetStyles sequence resets all colors and font attributes.
	ResetStyles = "\x1b[0m"
)

// Terminal manages terminal display modes, screen buffers, and cursor state.
type Terminal struct {
	writer   io.Writer
	altDepth int
	mu       sync.Mutex
}

var defaultTerminal = New(os.Stderr)

// Default returns the default process terminal manager writing to os.Stderr.
func Default() *Terminal {
	return defaultTerminal
}

// New creates a new Terminal instance writing to w.
func New(w io.Writer) *Terminal {
	if w == nil {
		w = os.Stderr
	}
	return &Terminal{
		writer: w,
	}
}

// RestoreCursor ensures the cursor is made visible.
func (t *Terminal) RestoreCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.writer, ShowCursor)
}

// ResetState resets alternate screen buffer, bracketed paste mode, cursor visibility, and styles.
func (t *Terminal) ResetState() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.altDepth = 0
	fmt.Fprint(t.writer, ExitAlternateScreen+DisableBracketedPaste+ResetStyles)
}

// EnterAltScreen switches the terminal to the alternate screen buffer.
// It is reference counted so nested components do not prematurely exit the alternate screen.
func (t *Terminal) EnterAltScreen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.altDepth == 0 {
		fmt.Fprint(t.writer, EnterAlternateScreen)
	}
	t.altDepth++
}

// ExitAltScreen decrements the alternate screen reference count and restores
// the primary screen buffer when the count reaches zero.
func (t *Terminal) ExitAltScreen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.altDepth > 0 {
		t.altDepth--
		if t.altDepth == 0 {
			fmt.Fprint(t.writer, ExitAlternateScreen)
		}
	}
}

// ForceExitAltScreen unconditionally exits the alternate screen buffer and makes the cursor visible.
func (t *Terminal) ForceExitAltScreen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.altDepth > 0 {
		t.altDepth = 0
		fmt.Fprint(t.writer, ExitAlternateScreen)
	}
}

// RecoverPanic restores terminal state if a panic occurs, invokes onPanic if non-nil,
// and re-panics with the recovered value.
func (t *Terminal) RecoverPanic(onPanic func()) {
	if r := recover(); r != nil {
		if onPanic != nil {
			onPanic()
		}
		t.mu.Lock()
		t.altDepth = 0
		fmt.Fprint(t.writer, ExitAlternateScreen+DisableBracketedPaste)
		t.mu.Unlock()
		panic(r)
	}
}

// ProbeDimensions queries terminal width and height without falling back to defaults.
// Returns (0, 0) if neither stdin, stdout, nor stderr is connected to a TTY.
func ProbeDimensions() (int, int) {
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && h > 0 {
		return w, h
	}
	if w, h, err := term.GetSize(int(os.Stderr.Fd())); err == nil && h > 0 {
		return w, h
	}
	return 0, 0
}

// GetDimensions returns terminal width and height, falling back to 80x24.
func GetDimensions() (int, int) {
	if w, h := ProbeDimensions(); w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}
