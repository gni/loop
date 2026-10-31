package render

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

// ConsoleControls describes one input frame. Hooks update cursor bookkeeping
// and compose the pinned footer without exposing global UI state.
type ConsoleControls struct {
	Width, Height  int
	Theme          style.UITheme
	PromptPrefix   string
	QueueLength    int
	InputLine      string
	InputPosition  int
	MaxInputRows   int
	DrawPrompt     bool
	InApproval     bool
	OldPasteOffset int
	Hint           string
	SetPasteOffset func(int)
	SetCursor      func(row, column int)
	OnScroll       func(int)
	DrawFooter     func(io.Writer)
}

func DrawConsoleControls(w io.Writer, options ConsoleControls) {
	termW, height := max(1, options.Width), options.Height
	promptStr, prefix := style.FormatPromptWithQueue(options.Theme, options.PromptPrefix, options.QueueLength)
	prefixLen := utf8.RuneCountInString(style.StripAnsi(prefix))
	layout := LayoutPrompt(options.InputLine, options.InputPosition, max(1, termW-prefixLen-1))
	maxRows := options.MaxInputRows
	if maxRows <= 0 {
		maxRows = 8
	}
	// Keep the transcript and its activity gap visible on small terminals.
	maxRows = max(1, min(maxRows, height-10))
	rows := min(len(layout.Rows), maxRows)
	offset := rows - 1
	oldOffset := options.OldPasteOffset
	if options.InApproval {
		offset = oldOffset
		rows = offset + 1
	}
	if options.SetPasteOffset != nil {
		options.SetPasteOffset(offset)
	}
	delta := offset - oldOffset
	oldBottom := style.ConsoleScrollBottom(height, oldOffset)
	var frame bytes.Buffer
	frame.WriteString("\x1b[?2026h\x1b[?25l")
	if delta > 0 && oldBottom > delta {
		fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[%d;1H\x1b[%dS", oldBottom, oldBottom, delta)
		if options.OnScroll != nil {
			options.OnScroll(-delta)
		}
	}
	for row := max(1, height-4-max(offset, oldOffset)); row <= height-2; row++ {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
	}
	bottom := style.ConsoleScrollBottom(height, offset)
	if delta < 0 {
		fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[1;1H\x1b[%dT", bottom, -delta)
		if options.OnScroll != nil {
			options.OnScroll(-delta)
		}
	}
	fmt.Fprintf(&frame, "\x1b[1;%dr", bottom)
	if options.DrawFooter != nil {
		options.DrawFooter(&frame)
	}
	start := max(0, layout.CursorRow-rows+1)
	start = min(start, max(0, len(layout.Rows)-rows))
	top := max(1, height-2-offset)
	indent := strings.Repeat(" ", prefixLen)
	if !options.InApproval {
		for i := 0; i < rows; i++ {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", top+i)
			index := start + i
			if index == 0 {
				frame.WriteString(promptStr)
			} else {
				frame.WriteString(indent)
			}
			line := layout.Rows[index].Text
			if line == "" && len(options.InputLine) == 0 && options.Hint != "" {
				line = style.NewStyle().Foreground(options.Theme.Border).Italic(true).Render(style.TruncateRunes(options.Hint, max(1, termW-prefixLen-1)))
			}
			frame.WriteString(line)
		}
	}
	cursorRow := min(height-2, max(top, top+layout.CursorRow-start))
	cursorCol := min(termW, max(1, 1+prefixLen+layout.CursorColumn))
	if options.SetCursor != nil {
		options.SetCursor(cursorRow, cursorCol)
	}
	fmt.Fprintf(&frame, "\x1b[%d;%dH", cursorRow, cursorCol)
	if options.DrawPrompt && !options.InApproval {
		frame.WriteString("\x1b[?25h")
	}
	frame.WriteString("\x1b[?2026l")
	_, _ = w.Write(frame.Bytes())
}
