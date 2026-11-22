package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

const (
	ansiNormal       = 0
	ansiEsc          = 1 // saw ESC
	ansiCSI          = 2 // saw ESC [
	ansiString       = 3 // saw ESC ] / P / X / ^ / _ (OSC, DCS, SOS, PM, APC)
	ansiIntermediate = 4 // saw ESC + 0x20..0x2F (3-byte+ intermediate/charset escape)
)

// PromptPreservingWriter wraps an io.Writer (typically os.Stderr) and ensures
// that streamed output stays inside the terminal's scrolling region.
// The scroll boundary follows style.ActivityGapLines, leaving a stable
// blank row between output and the pinned stats line. The cursor is restored
// to the prompt input at height-2 after every write.
//
// The pinned controls occupy the bottom five rows:
//   - height-4: stats line content
//   - height-3: ─── prompt ──────────────────────────────
//   - height-2: > (input line, cursor blinks here)
//   - height-1: ────────────────────────────────────────── (status separator)
//   - height:   status bar content
type PromptPreservingWriter struct {
	inner                 io.Writer
	raw                   io.Writer
	height                int
	printLine             int // current row inside the scroll region
	printCol              int // current column on printLine (1-based)
	promptCursorOffset    int
	promptCol             int // current column on promptLine for cursor restore (1-based)
	ansiState             int
	ansiPrevEsc           bool
	ansiBuf               []byte
	utf8Pending           []byte
	writeUTF8Pending      []byte
	activeSGR             style.SGRState
	cursorHidden          bool
	cursorAtPrompt        bool
	needsReposition       bool
	scrollCount           int
	restoreCursorToPrompt bool
	autoWrapPending       bool
	autoWrapDetached      bool
	pendingLineFeed       bool
	trailingNewlines      int
	needsSyntaxRefresh    bool // A live code line could not be recolored safely.
}

func (p *PromptPreservingWriter) blockedByAltOrMenu() bool {
	if terminal.Default().InAltScreen() {
		return true
	}
	ui := getUI()
	ui.StateMu.Lock()
	inMenu := ui.InInteractiveMenu
	ui.StateMu.Unlock()
	return inMenu
}

func (p *PromptPreservingWriter) getScrollBottom() int {
	return style.ConsoleScrollBottom(p.height, getUI().GetPasteLinesOffset())
}

func (p *PromptPreservingWriter) GetScrollBottom() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.getScrollBottom()
}

func NewPromptPreservingWriter(w io.Writer, height int) *PromptPreservingWriter {
	p := &PromptPreservingWriter{
		inner:                 w,
		raw:                   w,
		height:                height,
		printCol:              1,
		promptCol:             3,
		ansiState:             ansiNormal,
		cursorHidden:          false,
		cursorAtPrompt:        true,
		needsReposition:       true,
		restoreCursorToPrompt: true,
	}
	p.printLine = p.getScrollBottom()
	return p
}

func (p *PromptPreservingWriter) getPromptRow() int {
	return max(1, p.height-2-p.promptCursorOffset)
}

func (p *PromptPreservingWriter) moveCursorToPromptLocked(suffix string) {
	if p.blockedByAltOrMenu() {
		return
	}
	fmt.Fprintf(p.inner, "\x1b[%d;%dH%s", p.getPromptRow(), p.promptCol, suffix)
	p.cursorAtPrompt = true
	if p.autoWrapPending {
		p.autoWrapDetached = true
	}
}

func (p *PromptPreservingWriter) SetCursorHidden(hidden bool) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.cursorHidden = hidden
	if p.blockedByAltOrMenu() {
		return
	}
	if hidden {
		fmt.Fprint(p.inner, "\x1b[?25l")
	} else {
		p.moveCursorToPromptLocked("\x1b[?25h")
	}
}

func (p *PromptPreservingWriter) SetRestoreCursorToPrompt(restore bool) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.restoreCursorToPrompt = restore
	p.moveCursorToPromptLocked("")
}

func (p *PromptPreservingWriter) SetPromptCol(col int) {
	TerminalMu.Lock()
	p.promptCol = col
	TerminalMu.Unlock()
}

// SetPromptPositionLocked is called by the prompt renderer under TerminalMu.
// The cursor can be on any visible input row, independently of its top row.
func (p *PromptPreservingWriter) SetPromptPositionLocked(row, col int) {
	p.promptCursorOffset = max(0, p.height-2-row)
	p.promptCol = max(1, col)
}

// ResizeLocked synchronizes cursor bookkeeping before drawing a resized input.
func (p *PromptPreservingWriter) ResizeLocked(height int) {
	if h := getUI().GetChatHistory(); h != nil && h.Writer() == p && height > 0 {
		width, _ := GetTerminalSize()
		h.ResizeLocked(width, height)
		p.height = height
		return
	}
	if height <= 0 || height == p.height {
		return
	}
	delta := height - p.height
	p.height = height
	p.AdjustPrintLineLocked(delta)
	p.needsReposition = true
}

func (p *PromptPreservingWriter) AdjustPrintLineLocked(delta int) {
	p.printLine += delta
	scrollBottom := p.getScrollBottom()
	if p.printLine > scrollBottom {
		p.printLine = scrollBottom
	}
	if p.printLine < 1 {
		p.printLine = 1
	}
}

func (p *PromptPreservingWriter) AdjustPrintLine(delta int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.AdjustPrintLineLocked(delta)
}

func (p *PromptPreservingWriter) ForceRepositionLocked() {
	p.cursorAtPrompt = true
	scrollBottom := p.getScrollBottom()
	if p.printLine > scrollBottom || p.printLine < 1 {
		p.printLine = scrollBottom
	}
	p.printCol = 1
	p.autoWrapPending = false
	p.autoWrapDetached = false
	if p.blockedByAltOrMenu() {
		return
	}
	fmt.Fprintf(p.inner, "\x1b[%d;%dH", p.getPromptRow(), p.promptCol)
}

func (p *PromptPreservingWriter) ForceReposition() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.ForceRepositionLocked()
}

func (p *PromptPreservingWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.writeLocked(data)
}

func (p *PromptPreservingWriter) writeLocked(data []byte) (int, error) {
	if p.blockedByAltOrMenu() {
		return len(data), nil
	}
	origLen := len(data)
	if len(p.writeUTF8Pending) > 0 {
		combined := make([]byte, 0, len(p.writeUTF8Pending)+len(data))
		combined = append(combined, p.writeUTF8Pending...)
		combined = append(combined, data...)
		p.writeUTF8Pending = p.writeUTF8Pending[:0]
		data = combined
	}
	wireData, pending := splitIncompleteUTF8(data)
	if len(pending) > 0 {
		p.writeUTF8Pending = append(p.writeUTF8Pending[:0], pending...)
	}
	if len(wireData) == 0 {
		return origLen, nil
	}
	data = wireData

	_, termH := GetTerminalSize()
	p.ResizeLocked(termH)

	var buf bytes.Buffer
	buf.WriteString(terminal.BeginSyncOutput)

	if p.restoreCursorToPrompt {
		if !p.cursorAtPrompt {
			fmt.Fprintf(&buf, "\x1b[%d;%dH", p.getPromptRow(), p.promptCol)
		}
		buf.WriteString("\x1b7")
		fmt.Fprintf(&buf, "\x1b[1;%dr\x1b[%d;%dH", p.getScrollBottom(), p.printLine, p.printCol)
		p.cursorAtPrompt = false
		p.needsReposition = false
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	} else {
		fmt.Fprintf(&buf, "\x1b[1;%dr\x1b[%d;%dH", p.getScrollBottom(), p.printLine, p.printCol)
		p.needsReposition = false
		if p.autoWrapPending {
			p.autoWrapDetached = true
		}
	}

	if p.autoWrapPending && p.autoWrapDetached && !p.pendingLineFeed && p.firstTerminalActionIsGraphic(data) {
		buf.WriteString("\r\n")
		p.advancePrintLine()
		p.printCol = 1
		p.autoWrapPending = false
		p.autoWrapDetached = false
	}

	// A trailing LF at the bottom scrolls the last visible line upward.
	// Hold it until more output arrives so the layout's reserved gap is
	// the only empty row between the transcript and the pinned loader.
	if p.pendingLineFeed {
		buf.WriteString("\r\n")
		trailing := p.trailingNewlines
		p.trackPosition([]byte("\r\n"))
		p.trailingNewlines = trailing // This newline was already counted when deferred.
		p.pendingLineFeed = false
	}

	emitBody := func(body []byte) {
		if len(body) == 0 {
			return
		}
		if p.ansiState == ansiNormal && !p.activeSGR.IsZero() {
			buf.WriteString(p.activeSGR.Sequence())
		}
		buf.Write(body)
		p.trackPosition(body)
		if p.restoreCursorToPrompt && p.ansiState == ansiNormal && !p.activeSGR.IsZero() {
			buf.WriteString(terminal.ResetAttributes)
		}
	}

	if data[len(data)-1] == '\n' {
		body := data[:len(data)-1]
		emitBody(body)
		if p.printLine == p.getScrollBottom() {
			p.pendingLineFeed = true
			if p.trailingNewlines < style.ActivityGapLines+1 {
				p.trailingNewlines++
			}
			p.autoWrapPending = false
			p.autoWrapDetached = false
		} else {
			buf.WriteByte('\n')
			p.trackPosition([]byte{'\n'})
		}
	} else {
		emitBody(data)
	}

	if p.restoreCursorToPrompt {
		buf.WriteString("\x1b8")
		p.cursorAtPrompt = true
	} else {
		fmt.Fprintf(&buf, "\x1b[%d;%dH", p.printLine, p.printCol)
		p.cursorAtPrompt = false
	}
	if p.autoWrapPending {
		p.autoWrapDetached = true
	}
	buf.WriteString(terminal.EndSyncOutput)

	_, err := p.inner.Write(buf.Bytes())
	return origLen, err
}

func stepANSIStateByte(state int, prevEsc bool, b byte) (int, bool) {
	switch state {
	case ansiEsc:
		switch {
		case b == '[':
			return ansiCSI, false
		case b == ']' || b == 'P' || b == 'X' || b == '^' || b == '_':
			return ansiString, false
		case b >= 0x20 && b <= 0x2f:
			return ansiIntermediate, false
		default:
			return ansiNormal, false
		}
	case ansiCSI:
		if b >= 0x40 && b <= 0x7e {
			return ansiNormal, false
		}
		return ansiCSI, false
	case ansiString:
		if b == '\a' {
			return ansiNormal, false
		}
		if prevEsc && b == '\\' {
			return ansiNormal, false
		}
		return ansiString, b == '\x1b'
	case ansiIntermediate:
		if b >= 0x20 && b <= 0x2f {
			return ansiIntermediate, false
		}
		return ansiNormal, false
	default:
		return ansiNormal, false
	}
}

func (p *PromptPreservingWriter) stepANSIByte(b byte) {
	if p.ansiState == ansiEsc && b == '[' {
		p.ansiBuf = append(p.ansiBuf[:0], '\x1b', '[')
		p.ansiState = ansiCSI
		p.ansiPrevEsc = false
		return
	}
	if p.ansiState == ansiCSI {
		if len(p.ansiBuf) < 128 {
			p.ansiBuf = append(p.ansiBuf, b)
		}
		if b >= 0x40 && b <= 0x7e {
			if b == 'm' {
				p.activeSGR.Apply(string(p.ansiBuf))
			}
			p.ansiState = ansiNormal
			p.ansiPrevEsc = false
			p.ansiBuf = p.ansiBuf[:0]
		}
		return
	}
	p.ansiState, p.ansiPrevEsc = stepANSIStateByte(p.ansiState, p.ansiPrevEsc, b)
}

func splitIncompleteUTF8(b []byte) (complete, pending []byte) {
	n := len(b)
	if n == 0 {
		return b, nil
	}
	start := n - 1
	for start > 0 && n-start < utf8.UTFMax && (b[start]&0xc0) == 0x80 {
		start--
	}
	if b[start] >= utf8.RuneSelf && !utf8.FullRune(b[start:]) {
		return b[:start], b[start:]
	}
	return b, nil
}

func (p *PromptPreservingWriter) advanceGraphicCell(cells, termW int) {
	p.trailingNewlines = 0
	if p.autoWrapPending {
		p.advancePrintLine()
		p.printCol = 1
		p.autoWrapPending = false
		p.autoWrapDetached = false
	} else if termW > 0 && cells > 1 && p.printCol > 1 && p.printCol+cells-1 > termW {
		p.advancePrintLine()
		p.printCol = 1
		p.autoWrapPending = false
		p.autoWrapDetached = false
	}
	p.printCol += cells
	if termW > 0 && p.printCol > termW {
		p.printCol = termW
		p.autoWrapPending = true
		p.autoWrapDetached = false
	}
}

// trackPosition updates printCol, printLine, and activeSGR by analyzing written bytes.
// It uses cached terminal dimensions, grapheme cluster widths, 8-column tab stops,
// cross-chunk UTF-8 reassembly, and full ANSI escape tracking.
func (p *PromptPreservingWriter) trackPosition(data []byte) {
	if len(data) == 0 {
		return
	}
	termW, _ := GetTerminalSize()

	if len(p.utf8Pending) > 0 {
		combined := make([]byte, 0, len(p.utf8Pending)+len(data))
		combined = append(combined, p.utf8Pending...)
		combined = append(combined, data...)
		p.utf8Pending = p.utf8Pending[:0]
		data = combined
	}

	complete, pending := splitIncompleteUTF8(data)
	if len(pending) > 0 {
		p.utf8Pending = append(p.utf8Pending[:0], pending...)
	}

	var text string
	for i := 0; i < len(complete); {
		b := complete[i]
		if p.ansiState != ansiNormal {
			p.stepANSIByte(b)
			i++
			continue
		}
		if b == '\x1b' {
			p.ansiState = ansiEsc
			p.ansiPrevEsc = false
			i++
			continue
		}
		if b < utf8.RuneSelf {
			switch b {
			case '\n':
				if p.trailingNewlines < style.ActivityGapLines+1 {
					p.trailingNewlines++
				}
				p.printCol = 1
				p.autoWrapPending = false
				p.autoWrapDetached = false
				p.advancePrintLine()
			case '\r':
				p.printCol = 1
				p.autoWrapPending = false
				p.autoWrapDetached = false
			case '\b':
				p.autoWrapPending = false
				p.autoWrapDetached = false
				if p.printCol > 1 {
					p.printCol--
				}
			case '\t':
				p.autoWrapPending = false
				p.autoWrapDetached = false
				nextCol := p.printCol + style.TabAdvance(p.printCol-1, style.DefaultTabWidth)
				if termW > 0 && nextCol > termW {
					nextCol = termW
				}
				p.printCol = nextCol
			default:
				if b >= 0x20 && b != 0x7f {
					if i+1 < len(complete) && complete[i+1] >= utf8.RuneSelf {
						goto grapheme
					}
					p.advanceGraphicCell(1, termW)
				}
			}
			i++
			continue
		}
	grapheme:
		if text == "" {
			text = string(complete)
		}
		_, cells, size := style.NextGrapheme(text[i:])
		if size <= 0 {
			i++
			continue
		}
		if cells > 0 {
			p.advanceGraphicCell(cells, termW)
		}
		i += size
	}
}

func (p *PromptPreservingWriter) advancePrintLine() {
	scrollBottom := p.getScrollBottom()
	if p.printLine < scrollBottom {
		p.printLine++
		return
	}
	p.scrollCount++
}

func (p *PromptPreservingWriter) Height() int {
	return p.height
}

// PrintLineLocked returns the current scroll-region print row (caller holds TerminalMu).
func (p *PromptPreservingWriter) PrintLineLocked() int {
	return p.printLine
}

// PrintColLocked returns the current 1-based print column (caller holds TerminalMu).
func (p *PromptPreservingWriter) PrintColLocked() int {
	return p.printCol
}

// PendingLineFeedLocked reports whether a deferred line feed is pending (caller holds TerminalMu).
func (p *PromptPreservingWriter) PendingLineFeedLocked() bool {
	return p.pendingLineFeed
}

// AutoWrapPendingLocked reports whether VT100 pending wrap is active (caller holds TerminalMu).
func (p *PromptPreservingWriter) AutoWrapPendingLocked() bool {
	return p.autoWrapPending
}

// SetCursorStateLocked updates scroll-region print coordinates after a resize (caller holds TerminalMu).
func (p *PromptPreservingWriter) SetCursorStateLocked(printLine, printCol int, autoWrapPending bool) {
	p.printLine = printLine
	p.printCol = printCol
	p.autoWrapPending = autoWrapPending
	p.autoWrapDetached = autoWrapPending
}

// InnerLocked returns the inner output sink (caller holds TerminalMu).
func (p *PromptPreservingWriter) InnerLocked() io.Writer {
	return p.inner
}

// SetInnerLocked replaces the inner output sink (caller holds TerminalMu).
func (p *PromptPreservingWriter) SetInnerLocked(w io.Writer) {
	p.inner = w
}

// WriteLocked writes to the scroll region while TerminalMu is already held.
func (p *PromptPreservingWriter) WriteLocked(data []byte) (int, error) {
	return p.writeLocked(data)
}

func (p *PromptPreservingWriter) Unwrap() io.Writer {
	return p.raw
}

func (p *PromptPreservingWriter) GetPrintLine() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.printLine
}

func (p *PromptPreservingWriter) GetPrintCol() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.printCol
}

func (p *PromptPreservingWriter) EnsureNewline() {
	TerminalMu.Lock()
	needsNewline := !p.pendingLineFeed && (p.printCol != 1 || p.autoWrapPending)
	TerminalMu.Unlock()
	if needsNewline {
		_, _ = io.WriteString(p, "\n")
	}
}

// EnsureActivityGap leaves one empty chat row before a submitted prompt,
// accounting for line endings already emitted by the preceding reply.
func (p *PromptPreservingWriter) EnsureActivityGap() {
	TerminalMu.Lock()
	missing := style.ActivityGapLines + 1 - p.trailingNewlines
	TerminalMu.Unlock()
	if missing > 0 {
		_, _ = io.WriteString(p, strings.Repeat("\n", missing))
	}
}

func (p *PromptPreservingWriter) resetStreamParseState() {
	p.ansiState = ansiNormal
	p.ansiPrevEsc = false
	p.ansiBuf = p.ansiBuf[:0]
	p.utf8Pending = p.utf8Pending[:0]
	p.writeUTF8Pending = p.writeUTF8Pending[:0]
	p.activeSGR.Reset()
}

// BeginScrollEntry anchors a new output block to the bottom of the scrolling
// region. The previous block is completed first so no visible text is overwritten.
func (p *PromptPreservingWriter) BeginScrollEntry() {
	p.EnsureNewline()
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	_, termH := GetTerminalSize()
	p.ResizeLocked(termH)
	p.printLine = p.getScrollBottom()
	p.printCol = 1
	p.resetStreamParseState()
	p.resetAutoWrap()
}

// ResetAfterRedrawLocked synchronizes the writer with a full screen rebuild.
// Call while holding TerminalMu, after the rebuilt content has reached a fresh row.
func (p *PromptPreservingWriter) ResetAfterRedrawLocked(height int) {
	if height > 0 {
		p.height = height
	}
	p.printLine = p.getScrollBottom()
	p.printCol = 1
	p.resetStreamParseState()
	p.pendingLineFeed = false
	p.trailingNewlines = 0
	p.needsSyntaxRefresh = false
	p.resetAutoWrap()
}

// ResetAfterReplayLocked restores the position of an unfinished streaming line.
// The transcript contains display text and styling, never cursor movement.
// Call with TerminalMu held after building the transcript from row one.
func (p *PromptPreservingWriter) ResetAfterReplayLocked(height int, transcript []byte) {
	p.ResetAfterRedrawLocked(height)
	p.printLine = 1
	p.trackPosition(transcript)
	p.cursorAtPrompt = true
	if p.autoWrapPending {
		p.autoWrapDetached = true
	}
}

func (p *PromptPreservingWriter) GetScrollCount() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.scrollCount
}

func (p *PromptPreservingWriter) resetAutoWrap() {
	p.needsReposition = true
	p.autoWrapPending = false
	p.autoWrapDetached = false
}

func (p *PromptPreservingWriter) SetPrintLine(line int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.printLine = line
	p.pendingLineFeed = false
	p.resetAutoWrap()
}

func (p *PromptPreservingWriter) SetPrintCol(col int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	p.printCol = col
	p.pendingLineFeed = false
	p.resetAutoWrap()
}

func (p *PromptPreservingWriter) firstTerminalActionIsGraphic(data []byte) bool {
	if len(p.utf8Pending) > 0 {
		combined := make([]byte, 0, len(p.utf8Pending)+len(data))
		combined = append(combined, p.utf8Pending...)
		combined = append(combined, data...)
		data = combined
	}
	complete, _ := splitIncompleteUTF8(data)
	state := p.ansiState
	prevEsc := p.ansiPrevEsc
	var text string
	for i := 0; i < len(complete); {
		b := complete[i]
		if state != ansiNormal {
			state, prevEsc = stepANSIStateByte(state, prevEsc, b)
			i++
			continue
		}
		if b == '\x1b' {
			state = ansiEsc
			prevEsc = false
			i++
			continue
		}
		if b < utf8.RuneSelf {
			switch b {
			case '\n', '\r', '\b':
				return false
			case '\t':
				return true
			default:
				if b >= 0x20 && b != 0x7f {
					return true
				}
			}
			i++
			continue
		}
		if text == "" {
			text = string(complete)
		}
		_, cells, size := style.NextGrapheme(text[i:])
		if cells > 0 {
			return true
		}
		if size <= 0 {
			i++
		} else {
			i += size
		}
	}
	return false
}

// RepaintActiveScrollLine replaces the current unwrapped source row without
// reentering Write. Write manages scrolling, cursor restoration and deferred
// line feeds; sending cursor controls through it can move the active row.
// Like ReplaceScrollBlockBack, this uses an atomic cursor save/restore on the
// underlying terminal. It does not alter the transcript or the write cursor.
func (p *PromptPreservingWriter) RepaintActiveScrollLine(content string) bool {
	if strings.ContainsAny(content, "\r\n") {
		return false
	}
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if p.blockedByAltOrMenu() {
		return false
	}
	_, termH := GetTerminalSize()
	p.ResizeLocked(termH)
	if p.pendingLineFeed || p.autoWrapPending || p.printCol <= 1 ||
		p.printLine < 1 || p.printLine > p.getScrollBottom() {
		return false
	}
	var buf bytes.Buffer
	buf.WriteString(terminal.BeginSyncOutput + "\x1b7")
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s%s\x1b8%s", p.printLine, content, terminal.ResetAttributes, terminal.EndSyncOutput)
	_, err := p.inner.Write(buf.Bytes())
	return err == nil
}

// MarkSyntaxRefresh requests a history redraw after the current turn if a
// streamed source line could not be recolored safely in the live terminal.
func (p *PromptPreservingWriter) MarkSyntaxRefresh() {
	TerminalMu.Lock()
	p.needsSyntaxRefresh = true
	TerminalMu.Unlock()
}

// ConsumeSyntaxRefresh is called by the REPL after the turn has finished and
// the completed tool arguments have been added to conversation history.
func (p *PromptPreservingWriter) ConsumeSyntaxRefresh() bool {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	needed := p.needsSyntaxRefresh
	p.needsSyntaxRefresh = false
	return needed
}

// ReplaceScrollLineBack atomically replaces one visible line without changing
// the tracked stream position or repainting neighboring rows.
func (p *PromptPreservingWriter) ReplaceScrollLineBack(linesBack int, content string) bool {
	lines := strings.Split(content, "\n")
	return p.ReplaceScrollBlockBack(linesBack, lines)
}

// ReplaceScrollBlockBack atomically repaints existing scroll-region rows
// without advancing the output cursor or changing its tracked position.
func (p *PromptPreservingWriter) ReplaceScrollBlockBack(linesBack int, lines []string) bool {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if p.blockedByAltOrMenu() {
		return false
	}

	_, termH := GetTerminalSize()
	p.ResizeLocked(termH)
	if linesBack < 0 || len(lines) == 0 {
		return false
	}
	// The last newline at the scroll boundary is deferred by Write. Until
	// it is consumed, printLine still points at the last rendered row, not
	// the next one. Account for that when replacing already printed rows.
	if p.pendingLineFeed && linesBack > 0 {
		linesBack--
	}
	startRow := p.printLine - linesBack
	endRow := startRow + len(lines) - 1
	if startRow < 1 || endRow > p.getScrollBottom() {
		return false
	}

	var buf bytes.Buffer
	buf.WriteString(terminal.BeginSyncOutput + "\x1b7")
	for index, line := range lines {
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s%s", startRow+index, line, terminal.ResetAttributes)
	}
	buf.WriteString("\x1b8" + terminal.EndSyncOutput)
	_, err := p.inner.Write(buf.Bytes())
	return err == nil
}
