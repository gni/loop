package agent

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

type lockedStreamBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (l *lockedStreamBuffer) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	if l.mu != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	if l.buf == nil {
		return len(p), nil
	}
	return l.buf.Write(p)
}

type ansiParserState uint8

const (
	ansiGround ansiParserState = iota
	ansiEsc
	ansiCSI
	ansiString
	ansiStringEsc
	ansiIntermediate
)

type NewlineCounterWriter struct {
	io.Writer
	mu               sync.Mutex
	count            int
	col              int
	ansiState        ansiParserState
	pendingWrap      bool
	trailingNewlines int
	utf8Pending      []byte
}

func NewNewlineCounterWriter(w io.Writer) *NewlineCounterWriter {
	return &NewlineCounterWriter{Writer: w}
}

func (n *NewlineCounterWriter) Write(p []byte) (int, error) {
	termW, _ := style.GetTerminalSize()
	if termW <= 0 {
		termW = 80
	}

	n.mu.Lock()
	n.advanceLocked(p, termW)
	n.mu.Unlock()

	if n.Writer == nil {
		return len(p), nil
	}
	return n.Writer.Write(p)
}

func (n *NewlineCounterWriter) advanceLocked(p []byte, termW int) {
	if len(p) == 0 {
		return
	}
	if termW <= 0 {
		termW = 80
	}

	data := p
	if n.ansiState == ansiGround && len(n.utf8Pending) > 0 {
		combined := make([]byte, 0, len(n.utf8Pending)+len(p))
		combined = append(combined, n.utf8Pending...)
		combined = append(combined, p...)
		n.utf8Pending = n.utf8Pending[:0]
		data = combined
	}

	for i := 0; i < len(data); {
		b := data[i]
		switch n.ansiState {
		case ansiEsc:
			switch {
			case b == '[':
				n.ansiState = ansiCSI
			case b == ']', b == 'P', b == 'X', b == '^', b == '_':
				n.ansiState = ansiString
			case b >= 0x20 && b <= 0x2f:
				n.ansiState = ansiIntermediate
			case b == '\x1b':
				n.ansiState = ansiEsc
			default:
				n.ansiState = ansiGround
			}
			i++
			continue
		case ansiCSI:
			if b == '\x1b' {
				n.ansiState = ansiEsc
			} else if b >= 0x40 && b <= 0x7e {
				n.ansiState = ansiGround
			}
			i++
			continue
		case ansiString:
			if b == '\a' {
				n.ansiState = ansiGround
			} else if b == '\x1b' {
				n.ansiState = ansiStringEsc
			}
			i++
			continue
		case ansiStringEsc:
			if b == '\\' || b == '\a' {
				n.ansiState = ansiGround
			} else if b != '\x1b' {
				n.ansiState = ansiString
			}
			i++
			continue
		case ansiIntermediate:
			if b == '\x1b' {
				n.ansiState = ansiEsc
			} else if b < 0x20 || b > 0x2f {
				n.ansiState = ansiGround
			}
			i++
			continue
		}

		// Ground state.
		switch b {
		case '\x1b':
			n.ansiState = ansiEsc
			i++
			continue
		case '\n':
			n.count++
			n.col = 0
			n.pendingWrap = false
			if n.trailingNewlines < 2 {
				n.trailingNewlines++
			}
			i++
			continue
		case '\v', '\f':
			n.count++
			n.col = 0
			n.pendingWrap = false
			i++
			continue
		case '\r':
			n.col = 0
			n.pendingWrap = false
			i++
			continue
		case '\b':
			if n.pendingWrap {
				n.pendingWrap = false
				n.col = max(0, termW-2)
			} else if n.col > 0 {
				n.col--
			}
			i++
			continue
		case '\t':
			n.trailingNewlines = 0
			if n.pendingWrap || n.col >= termW {
				n.col = termW
				n.pendingWrap = true
			} else {
				n.col += style.TabAdvance(n.col, style.DefaultTabWidth)
				if n.col >= termW {
					n.col = termW
					n.pendingWrap = true
				}
			}
			i++
			continue
		}

		// Ignore other C0 control bytes and DEL.
		if b < 0x20 || b == 0x7f {
			i++
			continue
		}

		// Fast path for printable ASCII not followed by a non-ASCII combining/variation byte.
		if b < utf8.RuneSelf && (i+1 >= len(data) || data[i+1] < utf8.RuneSelf) {
			n.placeCellsLocked(1, termW)
			i++
			continue
		}

		// Collect the contiguous non-control segment so NextGrapheme never consumes
		// control characters or a trailing incomplete UTF-8 sequence split across writes.
		segEnd := i
		for segEnd < len(data) && data[segEnd] >= 0x20 && data[segEnd] != 0x7f {
			segEnd++
		}
		if segEnd == len(data) {
			startCheck := max(i, segEnd-3)
			for k := startCheck; k < segEnd; k++ {
				if utf8.RuneStart(data[k]) && !utf8.FullRune(data[k:segEnd]) {
					segEnd = k
					break
				}
			}
		}
		if segEnd == i {
			n.utf8Pending = append(n.utf8Pending[:0], data[i:]...)
			return
		}

		segment := string(data[i:segEnd])
		for off := 0; off < len(segment); {
			_, cw, sz := style.NextGrapheme(segment[off:])
			if sz <= 0 {
				off++
				continue
			}
			if cw > 0 {
				n.placeCellsLocked(cw, termW)
			}
			off += sz
		}
		i = segEnd
	}
}

func (n *NewlineCounterWriter) placeCellsLocked(w, termW int) {
	if w <= 0 {
		return
	}
	n.trailingNewlines = 0
	if n.pendingWrap || (n.col > 0 && n.col+w > termW) {
		n.count++
		n.col = 0
		n.pendingWrap = false
	}
	n.col += w
	if n.col >= termW {
		n.col = termW
		n.pendingWrap = true
	}
}

// EnsureNewline moves to the next line only if the previous output has not
// already done so.
func (n *NewlineCounterWriter) EnsureNewline() {
	n.mu.Lock()
	need := n.trailingNewlines == 0
	n.mu.Unlock()
	if need {
		_, _ = io.WriteString(n, "\n")
	}
}

// EnsureActivityGap separates completed output from the next tool header.
// The one newline terminating the previous line is not a blank row.
func (n *NewlineCounterWriter) EnsureActivityGap() {
	maxWrites := style.ActivityGapLines + 1
	for range maxWrites {
		n.mu.Lock()
		need := n.trailingNewlines < style.ActivityGapLines+1
		n.mu.Unlock()
		if !need {
			return
		}
		if _, err := io.WriteString(n, "\n"); err != nil {
			return
		}
	}
}

// ResetAfterReplay gives tracked rows the same origin as the rebuilt screen.
// The transcript contains display text and styling, without cursor movement.
func (n *NewlineCounterWriter) ResetAfterReplay(transcript []byte) {
	termW, _ := style.GetTerminalSize()
	if termW <= 0 {
		termW = 80
	}
	position := &NewlineCounterWriter{Writer: io.Discard}
	position.advanceLocked(transcript, termW)

	n.mu.Lock()
	n.count = position.count
	n.col = position.col
	n.ansiState = position.ansiState
	n.pendingWrap = position.pendingWrap
	n.trailingNewlines = position.trailingNewlines
	n.utf8Pending = append(n.utf8Pending[:0], position.utf8Pending...)
	n.mu.Unlock()
}

func (n *NewlineCounterWriter) GetCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

func (n *NewlineCounterWriter) TrailingNewlines() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.trailingNewlines
}

func (n *NewlineCounterWriter) Unwrap() io.Writer { return n.Writer }

func (n *NewlineCounterWriter) ReplaceScrollBlockBack(linesBack int, lines []string) bool {
	return forwardReplaceScrollBlockBack(n.Writer, linesBack, lines)
}

func (n *NewlineCounterWriter) RepaintActiveScrollLine(content string) bool {
	return forwardRepaintActiveScrollLine(n.Writer, content)
}

func (n *NewlineCounterWriter) ForceRepositionLocked() {
	forwardForceRepositionLocked(n.Writer)
}

type FallbackStreamRenderer struct {
	w io.Writer
}

func NewFallbackStreamRenderer(w io.Writer) *FallbackStreamRenderer {
	return &FallbackStreamRenderer{w: w}
}

func (f *FallbackStreamRenderer) Write(content string)                       { fmt.Fprint(f.w, content) }
func (f *FallbackStreamRenderer) WriteReasoning(string)                      {}
func (f *FallbackStreamRenderer) Flush()                                     {}
func (f *FallbackStreamRenderer) HasOutput() bool                            { return false }
func (f *FallbackStreamRenderer) StartToolCall(string, int)                  {}
func (f *FallbackStreamRenderer) SetToolTarget(string, int)                  {}
func (f *FallbackStreamRenderer) WriteToolCall(string)                       {}
func (f *FallbackStreamRenderer) GetToolTitleLineNumber(int) int             { return -1 }
func (f *FallbackStreamRenderer) DidStreamToolBody(int) bool                 { return false }
func (f *FallbackStreamRenderer) CompleteToolCall(int, string, string, bool) {}
func (f *FallbackStreamRenderer) GetReasoningDuration() float64              { return 0 }
func (f *FallbackStreamRenderer) SetPrompt(string)                           {}

type CustomTeeWriter struct {
	screen io.Writer
	buffer io.Writer
}

func NewCustomTeeWriter(screen, buffer io.Writer) *CustomTeeWriter {
	return &CustomTeeWriter{screen: screen, buffer: buffer}
}

func (c *CustomTeeWriter) Write(p []byte) (n int, err error) {
	n, err = c.screen.Write(p)
	if err == nil {
		_, _ = c.buffer.Write(p)
	}
	return n, err
}

func (c *CustomTeeWriter) Unwrap() io.Writer { return c.screen }

func (c *CustomTeeWriter) ReplaceScrollBlockBack(linesBack int, lines []string) bool {
	return forwardReplaceScrollBlockBack(c.screen, linesBack, lines)
}

func (c *CustomTeeWriter) RepaintActiveScrollLine(content string) bool {
	return forwardRepaintActiveScrollLine(c.screen, content)
}

func (c *CustomTeeWriter) ForceRepositionLocked() {
	forwardForceRepositionLocked(c.screen)
}

type scrollBlockReplacer interface {
	ReplaceScrollBlockBack(int, []string) bool
}

type activeScrollLinePainter interface {
	RepaintActiveScrollLine(string) bool
}

type lockedRepositioner interface {
	ForceRepositionLocked()
}

type writerUnwrapper interface {
	Unwrap() io.Writer
}

func forwardReplaceScrollBlockBack(w io.Writer, linesBack int, lines []string) bool {
	for w != nil {
		if sbr, ok := w.(scrollBlockReplacer); ok {
			return sbr.ReplaceScrollBlockBack(linesBack, lines)
		}
		u, ok := w.(writerUnwrapper)
		if !ok {
			return false
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return false
		}
		w = next
	}
	return false
}

func forwardRepaintActiveScrollLine(w io.Writer, content string) bool {
	for w != nil {
		if painter, ok := w.(activeScrollLinePainter); ok {
			return painter.RepaintActiveScrollLine(content)
		}
		u, ok := w.(writerUnwrapper)
		if !ok {
			return false
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return false
		}
		w = next
	}
	return false
}

func forwardForceRepositionLocked(w io.Writer) {
	for w != nil {
		if repo, ok := w.(lockedRepositioner); ok {
			repo.ForceRepositionLocked()
			return
		}
		u, ok := w.(writerUnwrapper)
		if !ok {
			return
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return
		}
		w = next
	}
}

func UnwrapWriter(w io.Writer) io.Writer {
	type unwrapper interface {
		Unwrap() io.Writer
	}
	for {
		if u, ok := w.(unwrapper); ok {
			unwrapped := u.Unwrap()
			if unwrapped == nil || unwrapped == w {
				return w
			}
			w = unwrapped
		} else {
			return w
		}
	}
}

func GetScreenWriter(w io.Writer) io.Writer {
	for w != nil {
		if ncw, ok := w.(*NewlineCounterWriter); ok {
			w = ncw.Writer
			continue
		}
		if tee, ok := w.(*CustomTeeWriter); ok {
			w = tee.screen
			continue
		}
		break
	}
	return w
}
