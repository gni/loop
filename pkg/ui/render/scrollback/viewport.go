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
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K%s%s", row+1, text, terminal.ResetAttributes)
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
