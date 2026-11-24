package terminal

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"

	"golang.org/x/term"
)

const (
	// ShowCursor makes the text cursor visible.
	ShowCursor = "\x1b[?25h"
	// HideCursor hides the text cursor.
	HideCursor = "\x1b[?25l"

	// DisableBracketedPaste disables bracketed paste mode.
	DisableBracketedPaste = "\x1b[?2004l"
	// EnableBracketedPaste separates pasted text from keyboard shortcuts.
	EnableBracketedPaste = "\x1b[?2004h"

	// EnterAlternateScreen switches the terminal to the alternate screen buffer.
	EnterAlternateScreen = "\x1b[?1049h\x1b[r\x1b[2J\x1b[H"
	// ExitAlternateScreen restores the primary screen buffer and shows the cursor.
	ExitAlternateScreen = "\x1b[?1049l\x1b[?25h"

	// EnableMouseTracking enables X10, button-event drag (?1002h), and SGR extended mouse reporting.
	EnableMouseTracking = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	// EnableMouseMotionTracking additionally enables DEC any-event motion reporting (?1003h) for hover tooltips.
	EnableMouseMotionTracking = "\x1b[?1000h\x1b[?1003h\x1b[?1006h"
	// DisableMouseTracking disables all standard and extended mouse reporting modes.
	DisableMouseTracking = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l"

	// EnableEnhancedKeys activates xterm modifyOtherKeys=2 and Kitty keyboard protocol level 1.
	EnableEnhancedKeys = "\x1b[>4;2m\x1b[>1u"
	// DisableEnhancedKeys pops Kitty keyboard flags and resets xterm modifyOtherKeys.
	DisableEnhancedKeys = "\x1b[<u\x1b[>4m"

	// BeginSyncOutput starts a DEC Mode 2026 synchronized update frame.
	BeginSyncOutput = "\x1b[?2026h"
	// EndSyncOutput commits a DEC Mode 2026 synchronized update frame.
	EndSyncOutput = "\x1b[?2026l"

	// ResetScrollRegion restores full-screen scrolling margins (DECSTBM).
	ResetScrollRegion = "\x1b[r"
	// ResetAttributes clears all SGR styling.
	ResetAttributes = "\x1b[0m"

	// FullCleanupSequence restores every terminal mode Loop may enable without leaving
	// the parent shell stuck in Kitty, modifyOtherKeys, mouse, sync, or alt-screen states.
	FullCleanupSequence = EndSyncOutput +
		DisableMouseTracking +
		DisableEnhancedKeys +
		DisableBracketedPaste +
		ResetScrollRegion +
		ResetAttributes +
		ExitAlternateScreen
)

type altScope struct {
	writer      io.Writer
	resumeMouse func()
}

// Terminal manages terminal display modes, screen buffers, cursor state, and cached dimensions.
type Terminal struct {
	writer io.Writer

	altStack []altScope

	pasteWriter     io.Writer
	pasteDepth      int
	pasteGeneration uint64

	mouseWriter     io.Writer
	mouseDepth      int
	mouseSuspended  int
	mouseGeneration uint64
	mouseMotion     bool
	mouseDisabled   bool

	keysWriter     io.Writer
	keysDepth      int
	keysGeneration uint64

	mu sync.Mutex
}

var (
	defaultTerminal = New(os.Stderr)
	cachedWidth     atomic.Int64
	cachedHeight    atomic.Int64
	dimensionsValid atomic.Bool
)

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

func unwrapDestination(w io.Writer) io.Writer {
	type unwrapper interface {
		Unwrap() io.Writer
	}
	for w != nil {
		u, ok := w.(unwrapper)
		if !ok {
			return w
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return w
		}
		w = next
	}
	return w
}

func (t *Terminal) resolveWriter(w io.Writer) io.Writer {
	w = unwrapDestination(w)
	if w == nil {
		return t.writer
	}
	return w
}

// RestoreCursor ensures the cursor is made visible.
func (t *Terminal) RestoreCursor() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = io.WriteString(t.writer, ShowCursor)
}

// BeginBracketedPaste keeps paste framing enabled until the outermost input
// scope closes. The returned cleanup is safe to call more than once.
func (t *Terminal) BeginBracketedPaste(w io.Writer) func() {
	t.mu.Lock()
	if t.pasteDepth == 0 {
		dest := t.resolveWriter(w)
		t.pasteWriter = dest
		_, _ = io.WriteString(dest, EnableBracketedPaste)
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
				if t.pasteWriter != nil {
					_, _ = io.WriteString(t.pasteWriter, DisableBracketedPaste)
				}
				t.pasteWriter = nil
			}
		})
	}
}

func (t *Terminal) enableMouseSequenceLocked() string {
	if t.mouseMotion {
		return EnableMouseMotionTracking
	}
	return EnableMouseTracking
}

// SetMouseEnabled globally permits or suppresses terminal mouse tracking
// (used by native_scroll mode). While disabled, mouse enable requests are
// suppressed so the terminal emulator retains native selection and scrollback.
func (t *Terminal) SetMouseEnabled(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	disabled := !enabled
	if t.mouseDisabled == disabled {
		return
	}
	t.mouseDisabled = disabled
	if t.mouseDepth > 0 && t.mouseSuspended == 0 && t.mouseWriter != nil {
		if disabled {
			_, _ = io.WriteString(t.mouseWriter, DisableMouseTracking)
		} else {
			_, _ = io.WriteString(t.mouseWriter, t.enableMouseSequenceLocked())
		}
	}
}

// MouseEnabled reports whether terminal mouse tracking is globally permitted.
func (t *Terminal) MouseEnabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.mouseDisabled
}

// SetMouseMotion toggles DEC any-event mouse motion reporting (?1003h) for hover tooltips.
func (t *Terminal) SetMouseMotion(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.mouseMotion == enabled {
		return
	}
	t.mouseMotion = enabled
	if !t.mouseDisabled && t.mouseDepth > 0 && t.mouseSuspended == 0 && t.mouseWriter != nil {
		if enabled {
			_, _ = io.WriteString(t.mouseWriter, EnableMouseMotionTracking)
		} else {
			_, _ = io.WriteString(t.mouseWriter, "\x1b[?1003l"+EnableMouseTracking)
		}
	}
}

// BeginMouseTracking enables mouse tracking until the returned cleanup is invoked.
func (t *Terminal) BeginMouseTracking(w io.Writer) func() {
	t.mu.Lock()
	if t.mouseDepth == 0 {
		dest := t.resolveWriter(w)
		t.mouseWriter = dest
		if !t.mouseDisabled && t.mouseSuspended == 0 {
			_, _ = io.WriteString(dest, t.enableMouseSequenceLocked())
		}
	}
	t.mouseDepth++
	generation := t.mouseGeneration
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if generation != t.mouseGeneration || t.mouseDepth == 0 {
				return
			}
			t.mouseDepth--
			if t.mouseDepth == 0 {
				if !t.mouseDisabled && t.mouseWriter != nil && t.mouseSuspended == 0 {
					_, _ = io.WriteString(t.mouseWriter, DisableMouseTracking)
				}
				t.mouseWriter = nil
			}
		})
	}
}

// SuspendMouseTracking temporarily disables active mouse tracking (for example
// during alt-screen menus, canonical slash commands, or external shell execution)
// and re-enables it when the returned resume function runs.
func (t *Terminal) SuspendMouseTracking() func() {
	t.mu.Lock()
	if !t.mouseDisabled && t.mouseSuspended == 0 && t.mouseDepth > 0 && t.mouseWriter != nil {
		_, _ = io.WriteString(t.mouseWriter, DisableMouseTracking)
	}
	t.mouseSuspended++
	generation := t.mouseGeneration
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.mouseSuspended == 0 {
				return
			}
			t.mouseSuspended--
			if !t.mouseDisabled && generation == t.mouseGeneration && t.mouseSuspended == 0 && t.mouseDepth > 0 && t.mouseWriter != nil {
				_, _ = io.WriteString(t.mouseWriter, t.enableMouseSequenceLocked())
			}
		})
	}
}

// BeginEnhancedKeys enables modifyOtherKeys and Kitty keyboard reporting until cleanup.
func (t *Terminal) BeginEnhancedKeys(w io.Writer) func() {
	t.mu.Lock()
	if t.keysDepth == 0 {
		dest := t.resolveWriter(w)
		t.keysWriter = dest
		_, _ = io.WriteString(dest, EnableEnhancedKeys)
	}
	t.keysDepth++
	generation := t.keysGeneration
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if generation != t.keysGeneration || t.keysDepth == 0 {
				return
			}
			t.keysDepth--
			if t.keysDepth == 0 {
				if t.keysWriter != nil {
					_, _ = io.WriteString(t.keysWriter, DisableEnhancedKeys)
				}
				t.keysWriter = nil
			}
		})
	}
}

// EnterAltScreen switches w (or the default writer) into the alternate screen buffer,
// suspends mouse tracking, and hides the cursor while tracking nesting depth.
func (t *Terminal) EnterAltScreen(w io.Writer) {
	t.mu.Lock()
	dest := t.resolveWriter(w)
	wasEmpty := len(t.altStack) == 0
	var resumeMouse func()
	if !t.mouseDisabled && t.mouseSuspended == 0 && t.mouseDepth > 0 && t.mouseWriter != nil {
		_, _ = io.WriteString(t.mouseWriter, DisableMouseTracking)
	}
	t.mouseSuspended++
	mouseGen := t.mouseGeneration
	resumeMouse = func() {
		if t.mouseSuspended > 0 {
			t.mouseSuspended--
			if !t.mouseDisabled && mouseGen == t.mouseGeneration && t.mouseSuspended == 0 && t.mouseDepth > 0 && t.mouseWriter != nil {
				_, _ = io.WriteString(t.mouseWriter, t.enableMouseSequenceLocked())
			}
		}
	}
	t.altStack = append(t.altStack, altScope{writer: dest, resumeMouse: resumeMouse})
	if wasEmpty {
		_, _ = io.WriteString(dest, EnterAlternateScreen+HideCursor)
	} else {
		_, _ = io.WriteString(dest, "\x1b[2J\x1b[H"+HideCursor)
	}
	t.mu.Unlock()
}

// ExitAltScreen decrements alternate screen nesting depth and restores the primary
// screen buffer, cursor visibility, and suspended mouse tracking when outermost.
func (t *Terminal) ExitAltScreen(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var scope altScope
	if len(t.altStack) > 0 {
		last := len(t.altStack) - 1
		scope = t.altStack[last]
		t.altStack = t.altStack[:last]
	}
	dest := unwrapDestination(w)
	if dest == nil {
		dest = scope.writer
	}
	if dest == nil {
		dest = t.writer
	}
	if len(t.altStack) == 0 {
		_, _ = io.WriteString(dest, EndSyncOutput+ExitAlternateScreen)
	}
	if scope.resumeMouse != nil {
		scope.resumeMouse()
	}
}

// InAltScreen reports whether the terminal is currently in the alternate screen buffer.
func (t *Terminal) InAltScreen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.altStack) > 0
}

// ForceExitAltScreen restores all active terminal modes without polluting redirected output.
func (t *Terminal) ForceExitAltScreen() { t.restoreModes("") }

func sameWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return false
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

func isRedirectedFile(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && !term.IsTerminal(int(file.Fd()))
}

func (t *Terminal) restoreModes(extra string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var targets []io.Writer
	addTarget := func(w io.Writer) {
		w = unwrapDestination(w)
		if w == nil || isRedirectedFile(w) {
			return
		}
		for _, existing := range targets {
			if sameWriter(existing, w) {
				return
			}
		}
		targets = append(targets, w)
	}

	if t.pasteDepth > 0 {
		addTarget(t.pasteWriter)
	}
	if t.mouseDepth > 0 {
		addTarget(t.mouseWriter)
	}
	if t.keysDepth > 0 {
		addTarget(t.keysWriter)
	}
	for _, scope := range t.altStack {
		addTarget(scope.writer)
	}
	if len(targets) == 0 {
		addTarget(t.writer)
	}

	t.pasteDepth = 0
	t.pasteWriter = nil
	t.pasteGeneration++

	t.mouseDepth = 0
	t.mouseSuspended = 0
	t.mouseWriter = nil
	t.mouseGeneration++

	t.keysDepth = 0
	t.keysWriter = nil
	t.keysGeneration++

	t.altStack = nil

	seq := FullCleanupSequence + extra
	for _, dest := range targets {
		_, _ = io.WriteString(dest, seq)
	}
}

// RecoverPanic restores terminal modes and preserves the original panic.
func (t *Terminal) RecoverPanic(onPanic func()) {
	if r := recover(); r != nil {
		if onPanic != nil {
			onPanic()
		}
		t.restoreModes("")
		panic(r)
	}
}

// WriteAtomic wraps a buffered render callback in a single synchronized output frame.
func WriteAtomic(w io.Writer, build func(*bytes.Buffer)) error {
	if w == nil || build == nil {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(BeginSyncOutput)
	build(&buf)
	buf.WriteString(EndSyncOutput)
	_, err := w.Write(buf.Bytes())
	return err
}

// ProbeDimensions queries terminal width and height directly via ioctl and refreshes
// the cached size. Returns (0, 0) if no stdio descriptor is connected to a TTY.
func ProbeDimensions() (int, int) {
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if f == nil {
			continue
		}
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			cachedWidth.Store(int64(w))
			cachedHeight.Store(int64(h))
			dimensionsValid.Store(true)
			return w, h
		}
	}
	dimensionsValid.Store(false)
	return 0, 0
}

// RefreshDimensions forces an immediate TTY size probe and updates the cached size.
// Call this from the SIGWINCH handler so hot render paths do not invoke ioctl per token.
func RefreshDimensions() (int, int) {
	if w, h := ProbeDimensions(); w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}

// GetDimensions returns the cached terminal width and height, probing once on first call
// and falling back to 80x24 when untended or attached to a pipe.
func GetDimensions() (int, int) {
	if dimensionsValid.Load() {
		w, h := int(cachedWidth.Load()), int(cachedHeight.Load())
		if w > 0 && h > 0 {
			return w, h
		}
	}
	if w, h := ProbeDimensions(); w > 0 && h > 0 {
		return w, h
	}
	return 80, 24
}
