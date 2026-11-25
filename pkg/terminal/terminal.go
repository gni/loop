package terminal

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

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

	// EnableMouseTracking enables X10 and SGR extended mouse reporting.
	EnableMouseTracking = "\x1b[?1000h\x1b[?1006h"
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

	// PointerShapePointer sets the OS mouse pointer to a pointing hand via OSC 22
	// (supported by Kitty, Ghostty, WezTerm, Foot, and compatible terminals).
	PointerShapePointer = "\x1b]22;pointer\x1b\\"
	// PointerShapeReset restores the terminal's default mouse pointer via OSC 22.
	PointerShapeReset = "\x1b]22;\x1b\\"

	// PushWindowTitle saves the terminal's current window/tab title onto the title stack.
	PushWindowTitle = "\x1b[22;0t"
	// PopWindowTitle restores the previously saved terminal window/tab title.
	PopWindowTitle = "\x1b[23;0t"

	// FullCleanupSequence restores every terminal mode Loop may enable without leaving
	// the parent shell stuck in Kitty, modifyOtherKeys, mouse, sync, or alt-screen states.
	FullCleanupSequence = EndSyncOutput +
		PointerShapeReset +
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
	pointerShape    string

	keysWriter     io.Writer
	keysDepth      int
	keysGeneration uint64

	titleWriter io.Writer
	titlePushed bool
	lastTitle   string

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

func (t *Terminal) resetPointerShapeLocked(w io.Writer) {
	if t.pointerShape == "" {
		return
	}
	t.pointerShape = ""
	if dest := t.resolveWriter(w); dest != nil {
		_, _ = io.WriteString(dest, PointerShapeReset)
	}
}

// SetPointerShape updates the terminal mouse pointer shape via OSC 22
// (for example "pointer" for a hand cursor over clickable buttons, or "" to
// restore the default pointer). Repeated calls with the same shape are no-ops.
func (t *Terminal) SetPointerShape(w io.Writer, shape string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if shape != "" && (t.mouseDisabled || t.mouseSuspended > 0) {
		shape = ""
	}
	if t.pointerShape == shape {
		return
	}
	dest := t.resolveWriter(w)
	if dest == nil {
		return
	}
	t.pointerShape = shape
	if shape == "" {
		_, _ = io.WriteString(dest, PointerShapeReset)
		return
	}
	_, _ = io.WriteString(dest, "\x1b]22;"+shape+"\x1b\\")
}

func sanitizeWindowTitle(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	inEsc := false
	inCSI := false
	inOSC := false
	prevSpace := false
	runes := 0
	const maxTitleRunes = 80

	for _, r := range title {
		if inOSC {
			if r == '\a' {
				inOSC = false
				inEsc = false
			} else if r == '\\' && inEsc {
				inOSC = false
				inEsc = false
			} else {
				inEsc = r == '\x1b'
			}
			continue
		}
		if inCSI {
			if r == '\x1b' {
				inCSI = false
				inEsc = true
			} else if r >= '@' && r <= '~' {
				inCSI = false
			}
			continue
		}
		if inEsc {
			switch r {
			case '[':
				inCSI = true
				inEsc = false
			case ']', 'P', 'X', '^', '_':
				inOSC = true
				inEsc = false
			default:
				if r < 0x20 || r > 0x2f {
					inEsc = false
				}
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			if b.Len() > 0 && !prevSpace && runes < maxTitleRunes {
				b.WriteByte(' ')
				prevSpace = true
				runes++
			}
			continue
		}
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			continue
		}
		if runes >= maxTitleRunes {
			break
		}
		b.WriteRune(r)
		prevSpace = false
		runes++
	}
	return strings.TrimSpace(b.String())
}

// PushTitle saves the terminal's existing tab/window title onto the xterm title stack.
func (t *Terminal) PushTitle(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	dest := t.resolveWriter(w)
	if dest == nil || isRedirectedFile(dest) || t.titlePushed {
		return
	}
	t.titleWriter = dest
	t.titlePushed = true
	_, _ = io.WriteString(dest, PushWindowTitle)
}

// SetTitle sets the terminal window and tab title via OSC 0.
// Control bytes are stripped and repeated identical titles are skipped.
func (t *Terminal) SetTitle(w io.Writer, title string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	dest := t.resolveWriter(w)
	if dest == nil || isRedirectedFile(dest) {
		return
	}
	clean := sanitizeWindowTitle(title)
	if clean == t.lastTitle {
		return
	}
	t.titleWriter = dest
	t.lastTitle = clean
	_, _ = io.WriteString(dest, "\x1b]0;"+clean+"\x07")
}

// RestoreTitle restores the previously pushed terminal title (or clears OSC 0).
func (t *Terminal) RestoreTitle(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.titlePushed && t.lastTitle == "" {
		return
	}
	dest := unwrapDestination(w)
	if dest == nil {
		dest = t.titleWriter
	}
	if dest == nil {
		dest = t.writer
	}
	pushed := t.titlePushed
	t.titlePushed = false
	t.lastTitle = ""
	t.titleWriter = nil
	if dest == nil || isRedirectedFile(dest) {
		return
	}
	if pushed {
		_, _ = io.WriteString(dest, PopWindowTitle)
	} else {
		_, _ = io.WriteString(dest, "\x1b]0;\x07")
	}
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
	if disabled {
		t.resetPointerShapeLocked(t.mouseWriter)
	}
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
	if !enabled {
		t.resetPointerShapeLocked(t.mouseWriter)
	}
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
				t.resetPointerShapeLocked(t.mouseWriter)
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
	t.resetPointerShapeLocked(t.mouseWriter)
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
	t.resetPointerShapeLocked(t.mouseWriter)
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
	if t.titlePushed || t.lastTitle != "" {
		addTarget(t.titleWriter)
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
	t.pointerShape = ""

	t.keysDepth = 0
	t.keysWriter = nil
	t.keysGeneration++

	restoreTitleSeq := ""
	if t.titlePushed {
		restoreTitleSeq = PopWindowTitle
	} else if t.lastTitle != "" {
		restoreTitleSeq = "\x1b]0;\x07"
	}
	t.titlePushed = false
	t.lastTitle = ""
	t.titleWriter = nil

	t.altStack = nil

	seq := FullCleanupSequence + restoreTitleSeq + extra
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
