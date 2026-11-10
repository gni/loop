package terminal

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sync"

	"golang.org/x/term"
)

const (
	// ShowCursor sequence makes the text cursor visible.
	ShowCursor = "\x1b[?25h"

	// DisableBracketedPaste sequence disables bracketed paste mode.
	DisableBracketedPaste = "\x1b[?2004l"
	// EnableBracketedPaste separates pasted text from keyboard shortcuts.
	EnableBracketedPaste = "\x1b[?2004h"
	// EnterAlternateScreen sequence switches terminal to alternate screen buffer.
	EnterAlternateScreen = "\x1b[?1049h\x1b[r\x1b[2J\x1b[H"
	// ExitAlternateScreen sequence restores primary screen buffer and shows cursor.
	ExitAlternateScreen = "\x1b[?1049l\x1b[?25h"
)

// Terminal manages terminal display modes, screen buffers, and cursor state.
type Terminal struct {
	writer          io.Writer
	altWriter       io.Writer
	altDepth        int
	pasteWriter     io.Writer
	pasteDepth      int
	pasteGeneration uint64
	mu              sync.Mutex
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

// BeginBracketedPaste keeps paste framing enabled until the outermost input
// scope closes. The returned cleanup is safe to call more than once.
func (t *Terminal) BeginBracketedPaste(w io.Writer) func() {
	t.mu.Lock()
	if t.pasteDepth == 0 {
		if w == nil {
			w = t.writer
		}
		t.pasteWriter = w
		fmt.Fprint(w, EnableBracketedPaste)
	}
	t.pasteDepth++
	generation := t.pasteGeneration
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if generation != t.pasteGeneration || t.pasteDepth == 0 {
				return
			}
			t.pasteDepth--
			if t.pasteDepth == 0 {
				fmt.Fprint(t.pasteWriter, DisableBracketedPaste)
				t.pasteWriter = nil
			}
		})
	}
}

// EnterAltScreen switches w (or the default writer) into the alternate screen buffer
// and hides the cursor, tracking nesting depth for signal and panic recovery.
func (t *Terminal) EnterAltScreen(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w == nil {
		w = t.writer
	}
	t.altWriter = w
	t.altDepth++
	fmt.Fprint(w, EnterAlternateScreen+"\x1b[?25l")
}

// ExitAltScreen decrements alternate screen nesting depth and restores the primary
// screen buffer and cursor visibility when the outermost caller exits.
func (t *Terminal) ExitAltScreen(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w == nil {
		w = t.altWriter
	}
	if w == nil {
		w = t.writer
	}
	if t.altDepth > 0 {
		t.altDepth--
	}
	if t.altDepth == 0 {
		t.altWriter = nil
		fmt.Fprint(w, "\x1b[?25h"+ExitAlternateScreen)
	}
}

// InAltScreen reports whether the terminal is currently in the alternate screen buffer.
func (t *Terminal) InAltScreen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.altDepth > 0
}

// ForceExitAltScreen restores active screen modes without polluting redirected output.
func (t *Terminal) ForceExitAltScreen() { t.restoreModes("") }

func (t *Terminal) restoreModes(extra string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pasteDepth > 0 {
		fmt.Fprint(t.pasteWriter, DisableBracketedPaste)
	}
	t.pasteDepth = 0
	t.pasteWriter = nil
	t.pasteGeneration++
	altW := t.altWriter
	t.altDepth = 0
	t.altWriter = nil
	same := altW != nil && reflect.TypeOf(altW).Comparable() && altW == t.writer
	if altW != nil && !same {
		fmt.Fprint(altW, ExitAlternateScreen+extra)
	}
	if file, ok := t.writer.(*os.File); ok && !term.IsTerminal(int(file.Fd())) && !same {
		return
	}
	if t.writer != nil {
		fmt.Fprint(t.writer, ExitAlternateScreen+extra)
	}
}

// RecoverPanic restores terminal modes and preserves the original panic.
func (t *Terminal) RecoverPanic(onPanic func()) {
	if r := recover(); r != nil {
		if onPanic != nil {
			onPanic()
		}
		t.restoreModes(DisableBracketedPaste)
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
