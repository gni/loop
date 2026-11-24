package history

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func ioWriterOf(r *interceptor.KeyInterceptorReader) io.Writer {
	if r == nil {
		return nil
	}
	return r.W
}

const (
	selectionAutoScrollInterval = 40 * time.Millisecond
	selectionMultiClickWindow   = 400 * time.Millisecond
	selectionToastDuration      = 3500 * time.Millisecond
)

var hasDesktopClipboard = terminal.HasDesktopClipboard

func formatSelectionNotice(lineCount, charCount int, localClipboard bool) string {
	if localClipboard {
		if lineCount > 1 {
			return fmt.Sprintf("Copied %d lines (%d chars) · Ctrl+X: native view · Esc: clear", lineCount, charCount)
		}
		return fmt.Sprintf("Copied %d chars · Ctrl+X: native view · Esc: clear", charCount)
	}
	if lineCount > 1 {
		return fmt.Sprintf("Selected %d lines (OSC 52 sent · Ctrl+X: native copy · Esc: clear)", lineCount)
	}
	return fmt.Sprintf("Selected %d chars (OSC 52 sent · Ctrl+X: native copy · Esc: clear)", charCount)
}

// SelectedPlainText returns the currently highlighted selection text (if any),
// with soft-wrapped rows joined cleanly.
func (h *ChatHistory) SelectedPlainText() string {
	if h == nil || h.closed.Load() {
		return ""
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	startRow, startCol, endRow, endCol, ok := h.selectionNormalizedLocked()
	if !ok || h.buffer == nil {
		return ""
	}
	return h.buffer.ExtractPlainText(startRow, startCol, endRow, endCol)
}

// FullRenderedTranscript returns all loaded rendered transcript rows from the
// scrollback buffer with soft-wrapped lines joined for clean native copy.
func (h *ChatHistory) FullRenderedTranscript() string {
	if h == nil || h.closed.Load() {
		return ""
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.buffer == nil {
		return ""
	}
	return h.buffer.ExtractStyledTranscript()
}

type chatSelection struct {
	active       bool
	hasRange     bool
	anchorRow    int
	anchorCol    int
	focusRow     int
	focusCol     int
	lastX        int
	lastY        int
	autoDir      int
	autoSpeed    int
	autoGen      uint64
	autoRunning  bool
	copiedNotice string
	copiedGen    uint64
	lastClickAt  time.Time
	lastClickRow int
	lastClickCol int
	clickCount   int
}

func (h *ChatHistory) setSelectionActiveLocked(active bool) {
	h.sel.active = active
	h.selActiveAtomic.Store(active)
	if !active {
		h.stopSelectionAutoScrollLocked()
	}
}

func (h *ChatHistory) stopSelectionAutoScrollLocked() {
	h.sel.autoDir = 0
	h.sel.autoSpeed = 0
	h.sel.autoRunning = false
	h.sel.autoGen++
}

func (h *ChatHistory) clearSelectionLocked() bool {
	hadVisible := h.sel.hasRange || h.sel.copiedNotice != ""
	h.setSelectionActiveLocked(false)
	h.sel.hasRange = false
	h.sel.copiedNotice = ""
	h.sel.copiedGen++
	h.sel.clickCount = 0
	return hadVisible
}

func (h *ChatHistory) currentViewTopLocked() int {
	if h.buffer == nil {
		return 0
	}
	if !h.browsing {
		return h.buffer.Base()
	}
	return min(max(0, h.top), h.buffer.Base())
}

func (h *ChatHistory) stickyQuestionVisibleLocked() bool {
	if !h.browsing || h.buffer == nil || h.a == nil || !h.a.LiveSettings().ShowStickyQuestion {
		return false
	}
	return h.activeQuestionIndexLocked(h.buffer.QuestionAnchors()) >= 0
}

func (h *ChatHistory) selectionNormalizedLocked() (startRow, startCol, endRow, endCol int, ok bool) {
	if !h.sel.hasRange {
		return 0, 0, 0, 0, false
	}
	startRow, startCol = h.sel.anchorRow, h.sel.anchorCol
	endRow, endCol = h.sel.focusRow, h.sel.focusCol
	if startRow > endRow || (startRow == endRow && startCol > endCol) {
		startRow, endRow = endRow, startRow
		startCol, endCol = endCol, startCol
	}
	return startRow, startCol, endRow, endCol, true
}

func (h *ChatHistory) beginSelectionLocked(x, y int) {
	if h.buffer == nil || y < 1 || y > h.buffer.Height {
		if h.clearSelectionLocked() {
			h.needsPaint = true
			h.paintLocked()
		}
		return
	}
	viewTop := h.currentViewTopLocked()
	row := viewTop + (y - 1)
	col := min(max(0, x-1), max(0, h.buffer.Width-1))

	now := time.Now()
	if now.Sub(h.sel.lastClickAt) <= selectionMultiClickWindow && h.sel.lastClickRow == row && absInt(h.sel.lastClickCol-col) <= 2 {
		h.sel.clickCount++
		if h.sel.clickCount > 3 {
			h.sel.clickCount = 1
		}
	} else {
		h.sel.clickCount = 1
	}
	h.sel.lastClickAt = now
	h.sel.lastClickRow = row
	h.sel.lastClickCol = col
	h.sel.copiedNotice = ""
	h.sel.copiedGen++
	h.sel.lastX = x
	h.sel.lastY = y

	hadPrior := h.sel.hasRange
	h.setSelectionActiveLocked(true)

	switch h.sel.clickCount {
	case 2:
		plain, _ := h.buffer.RowPlainText(row)
		wStart, wEnd := wordBoundsAtColumn(plain, col)
		h.sel.anchorRow, h.sel.anchorCol = row, wStart
		h.sel.focusRow, h.sel.focusCol = row, wEnd
		h.sel.hasRange = true
		h.needsPaint = true
		h.paintLocked()
	case 3:
		lStart, lEnd := h.buffer.LogicalLineBounds(row)
		h.sel.anchorRow, h.sel.anchorCol = lStart, 0
		h.sel.focusRow, h.sel.focusCol = lEnd, max(0, h.buffer.Width-1)
		h.sel.hasRange = true
		h.needsPaint = true
		h.paintLocked()
	default:
		h.sel.anchorRow, h.sel.anchorCol = row, col
		h.sel.focusRow, h.sel.focusCol = row, col
		h.sel.hasRange = false
		if hadPrior {
			h.needsPaint = true
			h.paintLocked()
		}
	}
}

func (h *ChatHistory) updateSelectionDrag(x, y int) {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.closed.Load() || !h.sel.active || h.buffer == nil {
		return
	}
	h.resizeLocked(ui.GetTerminalSize())
	h.sel.lastX = x
	h.sel.lastY = y

	minRow := 1
	if h.stickyQuestionVisibleLocked() {
		minRow = 2
	}
	maxRow := max(minRow, h.buffer.Height)

	// Detect edge/out-of-bounds drag to smoothly auto-scroll the viewport.
	autoDir := 0
	autoSpeed := 0
	if y >= maxRow && h.currentViewTopLocked() < h.buffer.Base() {
		autoDir = 1
		switch {
		case y >= maxRow+2:
			autoSpeed = 4
		case y == maxRow+1:
			autoSpeed = 2
		default:
			autoSpeed = 1
		}
	} else if y <= minRow && (h.currentViewTopLocked() > h.minimumTopLocked() || h.hasOlderLocked()) {
		autoDir = -1
		switch {
		case y <= minRow-2:
			autoSpeed = 4
		case y == minRow-1:
			autoSpeed = 2
		default:
			autoSpeed = 1
		}
	}

	if autoDir != 0 {
		h.scrollByLocked(autoDir * autoSpeed)
		h.sel.autoDir = autoDir
		h.sel.autoSpeed = autoSpeed
		h.ensureSelectionAutoScrollLocked()
	} else {
		h.stopSelectionAutoScrollLocked()
	}

	h.updateSelectionFocusLocked(x, y)
	h.needsPaint = true
	h.paintLocked()
}

func (h *ChatHistory) updateSelectionFocusLocked(x, y int) {
	if h.buffer == nil {
		return
	}
	minRow := 1
	if h.stickyQuestionVisibleLocked() {
		minRow = 2
	}
	maxRow := max(minRow, h.buffer.Height)
	viewTop := h.currentViewTopLocked()

	clampedY := min(max(minRow, y), maxRow)
	focusRow := viewTop + (clampedY - 1)
	focusCol := min(max(0, x-1), max(0, h.buffer.Width-1))
	if y > maxRow {
		focusCol = max(0, h.buffer.Width-1)
	} else if y < minRow {
		focusCol = 0
	}

	h.sel.focusRow = focusRow
	h.sel.focusCol = focusCol
	if h.sel.clickCount <= 1 {
		h.sel.hasRange = h.sel.anchorRow != h.sel.focusRow || h.sel.anchorCol != h.sel.focusCol
	} else {
		h.sel.hasRange = true
	}
}

func (h *ChatHistory) ensureSelectionAutoScrollLocked() {
	if h.sel.autoRunning || !h.sel.active || h.sel.autoDir == 0 {
		return
	}
	h.sel.autoRunning = true
	h.sel.autoGen++
	gen := h.sel.autoGen
	time.AfterFunc(selectionAutoScrollInterval, func() {
		h.stepSelectionAutoScroll(gen)
	})
}

func (h *ChatHistory) stepSelectionAutoScroll(gen uint64) {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.closed.Load() || !h.sel.active || h.sel.autoGen != gen || h.sel.autoDir == 0 || h.buffer == nil {
		return
	}
	h.resizeLocked(ui.GetTerminalSize())

	dir := h.sel.autoDir
	speed := max(1, h.sel.autoSpeed)
	beforeTop := h.currentViewTopLocked()
	h.scrollByLocked(dir * speed)
	afterTop := h.currentViewTopLocked()

	h.updateSelectionFocusLocked(h.sel.lastX, h.sel.lastY)
	if afterTop != beforeTop || h.pageJob != nil {
		h.needsPaint = true
		h.paintLocked()
	}

	canContinue := (dir > 0 && afterTop < h.buffer.Base()) ||
		(dir < 0 && (afterTop > h.minimumTopLocked() || h.hasOlderLocked()))
	if !canContinue {
		h.stopSelectionAutoScrollLocked()
		return
	}
	time.AfterFunc(selectionAutoScrollInterval, func() {
		h.stepSelectionAutoScroll(gen)
	})
}

func (h *ChatHistory) finishSelectionMouse(x, y int) {
	ui.TerminalMu.Lock()
	if h.closed.Load() || !h.sel.active {
		ui.TerminalMu.Unlock()
		return
	}
	h.setSelectionActiveLocked(false)
	if h.buffer != nil && x > 0 && y > 0 && h.sel.clickCount <= 1 {
		h.updateSelectionFocusLocked(x, y)
	}

	var copiedText string
	var destWriter = ioWriterOf(h.reader)
	if startRow, startCol, endRow, endCol, ok := h.selectionNormalizedLocked(); ok && h.buffer != nil {
		copiedText = h.buffer.ExtractPlainText(startRow, startCol, endRow, endCol)
		if copiedText != "" {
			if destWriter != nil {
				_ = terminal.WriteOSC52(destWriter, copiedText)
			}
			charCount := utf8.RuneCountInString(copiedText)
			lineCount := strings.Count(copiedText, "\n") + 1
			h.sel.copiedNotice = formatSelectionNotice(lineCount, charCount, hasDesktopClipboard())
			h.sel.copiedGen++
			toastGen := h.sel.copiedGen
			time.AfterFunc(selectionToastDuration, func() {
				ui.TerminalMu.Lock()
				defer ui.TerminalMu.Unlock()
				if h.closed.Load() || h.sel.copiedGen != toastGen {
					return
				}
				h.sel.copiedNotice = ""
				h.needsPaint = true
				h.paintLocked()
			})
		} else {
			h.sel.hasRange = false
		}
	}
	h.needsPaint = true
	h.paintLocked()
	ui.TerminalMu.Unlock()

	if copiedText != "" {
		go func(text string) {
			_ = terminal.CopyToDesktopClipboard(text)
		}(copiedText)
	}
}

func (h *ChatHistory) extendSelectionLocked(x, y int) {
	if h.buffer == nil || !h.sel.hasRange {
		return
	}
	h.sel.lastX = x
	h.sel.lastY = y
	h.sel.clickCount = 1
	h.updateSelectionFocusLocked(x, y)

	var copiedText string
	destWriter := ioWriterOf(h.reader)
	if startRow, startCol, endRow, endCol, ok := h.selectionNormalizedLocked(); ok {
		copiedText = h.buffer.ExtractPlainText(startRow, startCol, endRow, endCol)
		if copiedText != "" {
			if destWriter != nil {
				_ = terminal.WriteOSC52(destWriter, copiedText)
			}
			charCount := utf8.RuneCountInString(copiedText)
			lineCount := strings.Count(copiedText, "\n") + 1
			h.sel.copiedNotice = formatSelectionNotice(lineCount, charCount, hasDesktopClipboard())
			h.sel.copiedGen++
			toastGen := h.sel.copiedGen
			time.AfterFunc(selectionToastDuration, func() {
				ui.TerminalMu.Lock()
				defer ui.TerminalMu.Unlock()
				if h.closed.Load() || h.sel.copiedGen != toastGen {
					return
				}
				h.sel.copiedNotice = ""
				h.needsPaint = true
				h.paintLocked()
			})
		}
	}
	h.needsPaint = true
	h.paintLocked()

	if copiedText != "" {
		go func(text string) {
			_ = terminal.CopyToDesktopClipboard(text)
		}(copiedText)
	}
}

func (h *ChatHistory) selectionLineCountLocked() int {
	startRow, _, endRow, _, ok := h.selectionNormalizedLocked()
	if !ok {
		return 0
	}
	return endRow - startRow + 1
}

func (h *ChatHistory) shiftSelectionRowsLocked(delta int) {
	if !h.sel.active && !h.sel.hasRange {
		return
	}
	h.sel.anchorRow += delta
	h.sel.focusRow += delta
	if h.sel.anchorRow < 0 && h.sel.focusRow < 0 {
		h.clearSelectionLocked()
		return
	}
	h.sel.anchorRow = max(0, h.sel.anchorRow)
	h.sel.focusRow = max(0, h.sel.focusRow)
}

func (h *ChatHistory) insertSelectionRowsLocked(at, added int) {
	if added == 0 || (!h.sel.active && !h.sel.hasRange) {
		return
	}
	if h.sel.anchorRow >= at {
		h.sel.anchorRow += added
	}
	if h.sel.focusRow >= at {
		h.sel.focusRow += added
	}
}

func (h *ChatHistory) adjustSelectionRowsLocked(start, end, newEnd, delta, padding int) {
	if !h.sel.active && !h.sel.hasRange {
		return
	}
	for _, row := range []*int{&h.sel.anchorRow, &h.sel.focusRow} {
		if *row >= end {
			*row += delta + padding
		} else if *row >= start {
			*row = start + padding + min(*row-start, max(0, newEnd-start-1))
		} else {
			*row += padding
		}
	}
}

func selectionHighlightSeq(theme style.UITheme, light bool) string {
	// Verify true light vs dark polarity from Background luminance when available
	// (custom themes can declare light=true while using a dark canvas).
	if theme.Background != nil {
		r, g, b, _ := theme.Background.RGBA()
		luma := 0.2126*float64(r>>8) + 0.7152*float64(g>>8) + 0.0722*float64(b>>8)
		light = luma > 140
	}
	var bg color.Color
	var fg color.Color
	if light {
		if theme.Primary != nil && theme.Background != nil {
			bg = style.BlendColor(theme.Background, theme.Primary, 0.28)
		} else {
			bg = style.Color("#C7DFF2")
		}
		fg = style.Color("#111317")
	} else {
		if theme.Primary != nil && theme.Background != nil {
			bg = style.BlendColor(theme.Background, theme.Primary, 0.34)
		} else {
			bg = style.Color("#2D4A68")
		}
		if theme.Text != nil {
			fg = theme.Text
		} else {
			fg = style.Color("#F1F5F9")
		}
	}
	seq, _ := style.NewStyle().Background(bg).Foreground(fg).GetSequence()
	if seq == "" {
		return "\x1b[7m"
	}
	return seq
}

func (h *ChatHistory) selectionRowsLocked(rows []string) []string {
	startRow, startCol, endRow, endCol, ok := h.selectionNormalizedLocked()
	if !ok || len(rows) == 0 || h.buffer == nil {
		return rows
	}
	viewTop := h.currentViewTopLocked()
	if endRow < viewTop || startRow >= viewTop+len(rows) {
		return rows
	}

	var theme style.UITheme
	light := false
	if h.a != nil {
		live := h.a.LiveSettings()
		theme = style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
		light = style.IsLightTheme(live.Theme)
	} else {
		theme = style.GetTheme("studio")
	}
	selSeq := selectionHighlightSeq(theme, light)
	skipFirst := h.stickyQuestionVisibleLocked()

	out := rows
	copied := false
	for i, line := range rows {
		if i == 0 && skipFirst {
			continue
		}
		docRow := viewTop + i
		if docRow < startRow || docRow > endRow {
			continue
		}
		c0 := 0
		if docRow == startRow {
			c0 = max(0, startCol)
		}
		c1 := h.buffer.Width
		padEnd := docRow < endRow
		if docRow == endRow {
			c1 = min(h.buffer.Width, max(0, endCol+1))
			padEnd = false
		}
		if c1 <= c0 {
			continue
		}
		if !copied {
			out = append([]string(nil), rows...)
			copied = true
		}
		out[i] = highlightStyledRow(line, c0, c1, padEnd, selSeq)
	}
	return out
}

// highlightStyledRow applies selSeq across display columns [colStart, colEnd)
// while preserving original SGR state outside the selection and keeping wide
// graphemes / combining marks intact.
func highlightStyledRow(line string, colStart, colEnd int, padEmpty bool, selSeq string) string {
	var out strings.Builder
	out.Grow(len(line) + 32)
	var activeSGR style.SGRState
	inSel := false
	col := 0

	enterSel := func() {
		if !inSel {
			out.WriteString(terminal.ResetAttributes)
			out.WriteString(selSeq)
			inSel = true
		}
	}
	leaveSel := func() {
		if inSel {
			out.WriteString(terminal.ResetAttributes)
			if !activeSGR.IsZero() {
				out.WriteString(activeSGR.Sequence())
			}
			inSel = false
		}
	}

	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			end := style.SkipAnsiEscape(line, i)
			seq := line[i:end]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				activeSGR.Apply(seq)
				if !inSel {
					out.WriteString(seq)
				}
			} else {
				out.WriteString(seq)
			}
			i = end
			continue
		}
		cluster, cw, sz := style.NextGrapheme(line[i:])
		i += sz
		nextCol := col + cw

		selected := nextCol > colStart && col < colEnd
		if selected {
			enterSel()
		} else {
			leaveSel()
		}
		out.WriteString(cluster)
		col = nextCol
	}

	if padEmpty && col < colEnd {
		if col < colStart {
			leaveSel()
			out.WriteString(strings.Repeat(" ", colStart-col))
			col = colStart
		}
		if col < colEnd {
			enterSel()
			// Highlight one cell on blank lines inside a multi-line selection.
			out.WriteByte(' ')
		}
	}
	if inSel {
		out.WriteString(terminal.ResetAttributes)
	}
	return out.String()
}

func wordBoundsAtColumn(plain string, targetCol int) (startCol, endCol int) {
	type glyphSpan struct {
		start int
		end   int
		r     rune
	}
	var glyphs []glyphSpan
	col := 0
	for i := 0; i < len(plain); {
		cluster, cw, sz := style.NextGrapheme(plain[i:])
		i += sz
		r, _ := utf8.DecodeRuneInString(cluster)
		glyphs = append(glyphs, glyphSpan{start: col, end: max(col, col+cw-1), r: r})
		col += cw
	}
	if len(glyphs) == 0 {
		return targetCol, targetCol
	}
	idx := len(glyphs) - 1
	for i, g := range glyphs {
		if targetCol >= g.start && targetCol <= g.end {
			idx = i
			break
		}
		if targetCol < g.start {
			idx = max(0, i-1)
			break
		}
	}
	classOf := func(r rune) int {
		switch {
		case unicode.IsSpace(r):
			return 0
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			return 1
		default:
			return 2
		}
	}
	cls := classOf(glyphs[idx].r)
	left, right := idx, idx
	for left > 0 && classOf(glyphs[left-1].r) == cls {
		left--
	}
	for right+1 < len(glyphs) && classOf(glyphs[right+1].r) == cls {
		right++
	}
	return glyphs[left].start, glyphs[right].end
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
