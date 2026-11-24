package scrollback

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// ChatHistoryRows preserves styling and code indentation while wrapping a
// rendered transcript. Only SGR styling is retained; cursor controls stay out.
func ChatHistoryRows(text string, width int) []string {
	width = max(1, width)
	text = strings.TrimRight(text, "\r\n")
	var rows []string
	var line strings.Builder
	var active, emitted style.SGRState
	column := 0
	syncStyle := func() {
		if active == emitted {
			return
		}
		if !emitted.IsZero() {
			line.WriteString(terminal.ResetAttributes)
		}
		if !active.IsZero() {
			line.WriteString(active.Sequence())
		}
		emitted = active
	}
	finish := func() {
		if !emitted.IsZero() {
			line.WriteString(terminal.ResetAttributes)
			emitted.Reset()
		}
		rows = append(rows, line.String())
		line.Reset()
		column = 0
	}
	for i := 0; i < len(text); {
		if text[i] == 27 {
			end := style.SkipAnsiEscape(text, i)
			seq := text[i:end]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				active.Apply(seq)
			}
			i = end
			continue
		}
		if text[i] == '\n' {
			finish()
			i++
			continue
		}
		if text[i] == '\r' {
			i++
			continue
		}
		if text[i] == '\t' {
			cells := min(style.TabAdvance(column, style.DefaultTabWidth), width)
			if column > 0 && column+cells > width {
				finish()
				cells = min(style.TabAdvance(0, style.DefaultTabWidth), width)
			}
			syncStyle()
			line.WriteString(strings.Repeat(" ", cells))
			column += cells
			i++
			continue
		}
		cluster, cells, size := style.NextGrapheme(text[i:])
		i += size
		first, _ := utf8.DecodeRuneInString(cluster)
		if unicode.IsControl(first) {
			continue
		}
		if column > 0 && column+cells > width {
			finish()
		}
		syncStyle()
		line.WriteString(cluster)
		column += cells
	}
	finish()
	return rows
}

// DrawChatViewport updates changed rows and the counter on the stats row.
// Showing or hiding the counter never changes the chat area's height.
func DrawChatViewport(w io.Writer, rows, previous []string, hint, previousHint string) {
	DrawChatViewportAtomic(w, rows, previous, hint, previousHint, "")
}

type overlayCell struct {
	r   rune
	sgr string
}

// parseOverlayRow decodes a single-width styled viewport row into per-column
// cells for minimal cell-span diffing. Returns ok=false if any non-1-cell
// grapheme is present so the caller can safely fall back to full-row overwrite.
func parseOverlayRow(line string) ([]overlayCell, bool) {
	if line == "" {
		return nil, true
	}
	cells := make([]overlayCell, 0, 96)
	curSGR := ""
	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			end := style.SkipAnsiEscape(line, i)
			seq := line[i:end]
			if seq == "\x1b[0m" || seq == "\x1b[m" {
				curSGR = ""
			} else if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				curSGR = seq
			}
			i = end
			continue
		}
		cluster, cw, size := style.NextGrapheme(line[i:])
		i += size
		if cw != 1 {
			return nil, false
		}
		r, rSize := utf8.DecodeRuneInString(cluster)
		if rSize != len(cluster) {
			return nil, false
		}
		// Spaces carry no foreground ink, so normalize their SGR for clean diffs.
		sgr := curSGR
		if r == ' ' {
			sgr = ""
		}
		cells = append(cells, overlayCell{r: r, sgr: sgr})
	}
	for len(cells) > 0 && cells[len(cells)-1].r == ' ' {
		cells = cells[:len(cells)-1]
	}
	return cells, true
}

// DrawChatViewportOverlay updates only changed cells in place without clearing
// lines beforehand, without resetting scroll margins, and with the cursor hidden
// during the single atomic write. Cell-span diffing keeps consecutive frames
// compact (< 4KB) so terminals never blink, tear, or show cursor jump flicker.
func DrawChatViewportOverlay(w io.Writer, rows, previous []string) {
	if w == nil {
		return
	}
	var frame bytes.Buffer
	sync := !terminal.InSyncFrame(w)
	started := false
	begin := func() {
		if started {
			return
		}
		started = true
		frame.Grow(3072)
		if sync {
			frame.WriteString(terminal.BeginSyncOutput)
		}
		frame.WriteString(terminal.HideCursor)
		frame.WriteString("\x1b7")
	}

	havePrev := len(previous) == len(rows)
	for row, text := range rows {
		if havePrev && text == previous[row] {
			continue
		}
		if !havePrev {
			begin()
			fmt.Fprintf(&frame, "\x1b[%d;1H%s%s\x1b[K", row+1, text, terminal.ResetAttributes)
			continue
		}

		cur, okCur := parseOverlayRow(text)
		prev, okPrev := parseOverlayRow(previous[row])
		if !okCur || !okPrev {
			begin()
			fmt.Fprintf(&frame, "\x1b[%d;1H%s%s\x1b[K", row+1, text, terminal.ResetAttributes)
			continue
		}
		maxLen := max(len(cur), len(prev))
		if maxLen == 0 {
			continue
		}

		activeSGR := ""
		col := 0
		for col < maxLen {
			var cc, pc overlayCell
			if col < len(cur) {
				cc = cur[col]
			} else {
				cc = overlayCell{r: ' '}
			}
			if col < len(prev) {
				pc = prev[col]
			} else {
				pc = overlayCell{r: ' '}
			}
			if cc == pc {
				col++
				continue
			}

			// If every remaining cell past len(cur) is now blank, clear tail with \x1b[K.
			if col >= len(cur) {
				begin()
				if activeSGR != "" {
					frame.WriteString(terminal.ResetAttributes)
					activeSGR = ""
				}
				fmt.Fprintf(&frame, "\x1b[%d;%dH\x1b[K", row+1, col+1)
				break
			}

			// Emit a contiguous run of changed cells (bridging tiny 1-2 cell gaps
			// when cheaper than emitting a new CUP escape sequence).
			begin()
			fmt.Fprintf(&frame, "\x1b[%d;%dH", row+1, col+1)
			for col < len(cur) {
				cc = cur[col]
				if col < len(prev) && cc == prev[col] {
					// Check if the next cell is also unchanged; if so, end span here.
					if col+1 >= len(cur) || (col+1 < len(prev) && cur[col+1] == prev[col+1]) {
						break
					}
				}
				if cc.r != ' ' && cc.sgr != activeSGR {
					if cc.sgr == "" {
						frame.WriteString(terminal.ResetAttributes)
					} else {
						frame.WriteString(cc.sgr)
					}
					activeSGR = cc.sgr
				}
				frame.WriteRune(cc.r)
				col++
			}
		}
		if activeSGR != "" {
			frame.WriteString(terminal.ResetAttributes)
		}
	}

	if !started {
		return
	}
	frame.WriteString("\x1b8")
	frame.WriteString(terminal.ShowCursor)
	if sync {
		frame.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(frame.Bytes())
}

// DrawChatViewportAtomic updates changed rows and the stats/hint row in a single
// synchronized frame. When hint is empty and fallbackStats is non-empty,
// fallbackStats is painted on the stats row in the same write so leaving history
// or search mode never produces a one-frame blank flash.
func DrawChatViewportAtomic(w io.Writer, rows, previous []string, hint, previousHint, fallbackStats string) {
	if w == nil {
		return
	}
	var frame bytes.Buffer
	sync := !terminal.InSyncFrame(w)
	begin := func() {
		if frame.Len() == 0 {
			if sync {
				frame.WriteString(terminal.BeginSyncOutput)
			}
			frame.WriteString("\x1b7")
			fmt.Fprintf(&frame, "\x1b[1;%dr", max(1, len(rows)))
		}
	}
	for row, text := range rows {
		if len(previous) == len(rows) && text == previous[row] {
			continue
		}
		begin()
		fmt.Fprintf(&frame, "\x1b[%d;1H%s%s\x1b[K", row+1, text, terminal.ResetAttributes)
	}
	statsText := hint
	if statsText == "" {
		statsText = fallbackStats
	}
	statsChanged := len(previous) != len(rows) || hint != previousHint || (frame.Len() > 0 && hint == "" && fallbackStats != "")
	if statsChanged {
		begin()
		for row := len(rows) + 1; row <= len(rows)+style.ActivityGapLines; row++ {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
		}
		statsRow := len(rows) + style.ActivityGapLines + 1
		if statsText != "" {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K%s%s", statsRow, statsText, terminal.ResetAttributes)
		} else {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", statsRow)
		}
	}
	if frame.Len() == 0 {
		return
	}
	frame.WriteString("\x1b8")
	if sync {
		frame.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(frame.Bytes())
}
