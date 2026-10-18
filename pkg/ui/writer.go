package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/domain/output"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
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
	height                int
	printLine             int // current row inside the scroll region
	printCol              int // current column on printLine (1-based)
	promptCol             int // current column on promptLine for cursor restore (1-based)
	ansiState             int // 0: normal, 1: saw ESC, 2: saw ESC [, 3: saw ESC ]
	cursorHidden          bool
	cursorAtPrompt        bool
	needsReposition       bool
	scrollCount           int
	restoreCursorToPrompt bool
	autoWrapPending       bool
	autoWrapDetached      bool
	pendingLineFeed       bool
}

func (p *PromptPreservingWriter) getOffsetRow(offset int) int {
	row := p.height - offset - getUI().PasteLinesOffset
	if row < 1 {
		return 1
	}
	return row
}

func (p *PromptPreservingWriter) getScrollBottom() int {
	return style.ConsoleScrollBottom(p.height, getUI().PasteLinesOffset)
}

func (p *PromptPreservingWriter) GetScrollBottom() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	return p.getScrollBottom()
}

func NewPromptPreservingWriter(w io.Writer, height int) *PromptPreservingWriter {
	p := &PromptPreservingWriter{
		inner:                 w,
		height:                height,
		printCol:              1,
		promptCol:             3,
		ansiState:             0,
		cursorHidden:          false,
		cursorAtPrompt:        true,
		needsReposition:       true,
		restoreCursorToPrompt: true,
	}
	p.printLine = p.getScrollBottom()
	return p
}

func (p *PromptPreservingWriter) getPromptRow() int {
	return p.getOffsetRow(2)
}

func (p *PromptPreservingWriter) moveCursorToPromptLocked(suffix string) {
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

	if h := getRealTerminalHeight(); h > 0 && h != p.height {
		diff := h - p.height
		p.height = h
		p.AdjustPrintLineLocked(diff)
		if diff != 0 {
			p.needsReposition = true
		}
	}

	var buf bytes.Buffer
	buf.WriteString("\x1b[?2026h")

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

	if p.autoWrapPending && p.autoWrapDetached && !p.pendingLineFeed && firstTerminalActionIsGraphic(data, p.ansiState) {
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
		p.trackPosition([]byte("\r\n"))
		p.pendingLineFeed = false
	}
	if data[len(data)-1] == '\n' {
		body := data[:len(data)-1]
		buf.Write(body)
		p.trackPosition(body)
		if p.printLine == p.getScrollBottom() {
			p.pendingLineFeed = true
			p.autoWrapPending = false
			p.autoWrapDetached = false
		} else {
			buf.WriteByte('\n')
			p.trackPosition([]byte{'\n'})
		}
	} else {
		buf.Write(data)
		p.trackPosition(data)
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
	buf.WriteString("\x1b[?2026l")

	_, err := p.inner.Write(buf.Bytes())
	return len(data), err
}

func stepANSIState(state int, r rune) int {
	switch state {
	case 1:
		switch r {
		case '[':
			return 2
		case ']':
			return 3
		default:
			return 0
		}
	case 2:
		if r >= 0x40 && r <= 0x7E {
			return 0
		}
		return 2
	case 3:
		if r == '\x07' || r == '\\' {
			return 0
		}
		return 3
	default:
		return 0
	}
}

// trackPosition updates printCol by analyzing the written bytes.
// On newline, printCol resets to 1 (the terminal scrolls within the region).
// On carriage return, printCol resets to 1 without scrolling.
func (p *PromptPreservingWriter) trackPosition(data []byte) {
	termW, _ := GetTerminalSize()

	for _, r := range string(data) {
		if p.ansiState != 0 {
			p.ansiState = stepANSIState(p.ansiState, r)
			continue
		}
		if r == '\x1b' {
			p.ansiState = 1
			continue
		}

		switch r {
		case '\n':
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
			nextCol := ((p.printCol-1)/8*8 + 8) + 1
			if termW > 0 && nextCol > termW {
				nextCol = termW
			}
			p.printCol = nextCol
		default:
			if r >= 32 && r != '\uFFFD' {
				if p.autoWrapPending {
					p.advancePrintLine()
					p.printCol = 1
					p.autoWrapPending = false
					p.autoWrapDetached = false
				}
				p.printCol++
				if termW > 0 && p.printCol > termW {
					p.printCol = termW
					p.autoWrapPending = true
					p.autoWrapDetached = false
				}
			}
		}
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

func (p *PromptPreservingWriter) Unwrap() io.Writer {
	return p.inner
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

// BeginScrollEntry anchors a new output block to the bottom of the scrolling
// region. The previous block is completed first so no visible text is overwritten.
func (p *PromptPreservingWriter) BeginScrollEntry() {
	p.EnsureNewline()
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if height := getRealTerminalHeight(); height > 0 {
		p.height = height
	}
	p.printLine = p.getScrollBottom()
	p.printCol = 1
	p.ansiState = 0
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
	p.ansiState = 0
	p.pendingLineFeed = false
	p.resetAutoWrap()
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

func firstTerminalActionIsGraphic(data []byte, initialANSIState int) bool {
	state := initialANSIState
	for _, r := range string(data) {
		if state != 0 {
			state = stepANSIState(state, r)
			continue
		}
		if r == '\x1b' {
			state = 1
			continue
		}
		switch r {
		case '\n', '\r', '\b':
			return false
		case '\t':
			return true
		default:
			if r >= 32 && r != '\uFFFD' {
				return true
			}
		}
	}
	return false
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

	if height := getRealTerminalHeight(); height > 0 && height != p.height {
		delta := height - p.height
		p.height = height
		p.AdjustPrintLineLocked(delta)
	}
	if linesBack < 0 || len(lines) == 0 {
		return false
	}
	startRow := p.printLine - linesBack
	endRow := startRow + len(lines) - 1
	if startRow < 1 || endRow > p.getScrollBottom() {
		return false
	}

	var buf bytes.Buffer
	buf.WriteString("\x1b[?2026h\x1b7")
	for index, line := range lines {
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s", startRow+index, line)
	}
	buf.WriteString("\x1b8\x1b[?2026l")
	_, err := p.inner.Write(buf.Bytes())
	return err == nil
}

func getRealTerminalHeight() int {
	_, h := terminal.ProbeDimensions()
	return h
}

// OutputGuard is the shared single-writer output guard from pkg/domain/output,
// used in passthrough mode: one writer owns the terminal, extensions can take it
// over (their output is diverted to stderr while taken over), and Flush drains
// pending writes before control is handed back.
type OutputGuard = output.OutputGuard

func NewOutputGuard(w io.Writer) *OutputGuard {
	return output.NewPassthrough(w, os.Stderr)
}
