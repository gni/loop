package render

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

// ChatHistoryRows preserves styling and code indentation while wrapping a
// rendered transcript. Only SGR styling is retained; cursor controls stay out.
func ChatHistoryRows(text string, width int) []string {
	width = max(1, width)
	text = strings.TrimRight(text, "\r\n")
	var rows []string
	var line strings.Builder
	var active string
	column := 0
	finish := func() {
		rows = append(rows, line.String()+"\x1b[0m")
		line.Reset()
		line.WriteString(active)
		column = 0
	}
	for i := 0; i < len(text); {
		if text[i] == 27 {
			end := style.SkipAnsiEscape(text, i)
			seq := text[i:end]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					active = ""
				} else {
					active += seq
				}
				line.WriteString(seq)
			}
			i = end
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		if r == '\n' {
			finish()
			continue
		}
		if r == '\r' {
			continue
		}
		cells := promptRuneWidth(r)
		if r == '\t' {
			cells = min(4-column%4, width)
		}
		if unicode.IsControl(r) && r != '\t' {
			continue
		}
		if column > 0 && column+cells > width {
			finish()
		}
		if r == '\t' {
			line.WriteString(strings.Repeat(" ", cells))
		} else {
			line.WriteRune(r)
		}
		column += cells
	}
	finish()
	return rows
}

// DrawChatViewport updates changed rows and the counter on the stats row.
// Showing or hiding the counter never changes the chat area's height.
func DrawChatViewport(w io.Writer, rows, previous []string, hint, previousHint string) {
	var frame bytes.Buffer
	begin := func() {
		if frame.Len() == 0 {
			frame.WriteString("\x1b[?2026h\x1b7")
			fmt.Fprintf(&frame, "\x1b[1;%dr", len(rows))
		}
	}
	for row, text := range rows {
		if len(previous) == len(rows) && text == previous[row] {
			continue
		}
		begin()
		fmt.Fprintf(&frame, "\x1b[%d;1H%s\x1b[0m\x1b[K", row+1, text)
	}
	if len(previous) != len(rows) || hint != previousHint {
		begin()
		for row := len(rows) + 1; row <= len(rows)+style.ActivityGapLines; row++ {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H%s\x1b[0m\x1b[K", len(rows)+style.ActivityGapLines+1, hint)
	}
	if frame.Len() == 0 {
		return
	}
	frame.WriteString("\x1b8\x1b[?2026l")
	_, _ = w.Write(frame.Bytes())
}
