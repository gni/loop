package render

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"loop/pkg/terminal"
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
	if w == nil || options.Height <= 0 {
		return
	}
	termW, height := max(1, options.Width), options.Height
	promptStr, prefix := style.FormatPromptWithQueue(options.Theme, options.PromptPrefix, options.QueueLength)
	prefixLen := style.CellWidth(prefix)
	promptColumns := max(1, termW-prefixLen-1)
	layout := LayoutPrompt(options.InputLine, options.InputPosition, promptColumns)
	maxRows := options.MaxInputRows
	if maxRows <= 0 {
		maxRows = 8
	}
	if height < 6 {
		maxRows = 1
	} else {
		// Keep the transcript and its activity gap visible on small terminals.
		maxRows = max(1, min(maxRows, height-10))
	}
	rows := min(len(layout.Rows), maxRows)
	offset := rows - 1
	oldOffset := max(0, options.OldPasteOffset)
	if height < 6 {
		offset = 0
		rows = 1
		oldOffset = 0
	}
	if options.InApproval {
		offset = oldOffset
		rows = offset + 1
	}
	if options.SetPasteOffset != nil {
		options.SetPasteOffset(offset)
	}
	delta := offset - oldOffset
	oldBottom := style.ConsoleScrollBottom(height, oldOffset)
	bottom := style.ConsoleScrollBottom(height, offset)

	var frame bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		frame.WriteString(terminal.BeginSyncOutput)
	}

	if options.InApproval {
		// Preserve the active approval/overlay area and cursor instead of clearing it.
		frame.WriteString("\x1b7")
		fmt.Fprintf(&frame, "\x1b[1;%dr", bottom)
		if options.DrawFooter != nil && height >= 6 {
			options.DrawFooter(WrapSyncFrame(&frame))
		}
		frame.WriteString("\x1b8")
		if sync {
			frame.WriteString(terminal.EndSyncOutput)
		}
		_, _ = w.Write(frame.Bytes())
		return
	}

	frame.WriteString(terminal.HideCursor)
	if delta > 0 && oldBottom > delta {
		fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[%d;1H\x1b[%dS", oldBottom, oldBottom, delta)
		if options.OnScroll != nil {
			options.OnScroll(-delta)
		}
	}
	if height >= 6 {
		clearStart := max(1, height-4-style.ActivityGapLines-max(offset, oldOffset))
		clearEnd := min(height, height-2)
		for row := clearStart; row <= clearEnd; row++ {
			fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", row)
		}
	}
	if delta < 0 {
		fmt.Fprintf(&frame, "\x1b[1;%dr\x1b[1;1H\x1b[%dT", bottom, -delta)
		if options.OnScroll != nil {
			options.OnScroll(-delta)
		}
	}
	fmt.Fprintf(&frame, "\x1b[1;%dr", bottom)
	if options.DrawFooter != nil && height >= 4 {
		options.DrawFooter(WrapSyncFrame(&frame))
	}
	start := max(0, layout.CursorRow-rows+1)
	start = min(start, max(0, len(layout.Rows)-rows))
	top := max(1, height-2-offset)
	maxPromptRow := max(top, min(height, height-2))
	indent := strings.Repeat(" ", min(prefixLen, max(0, termW-1)))
	for i := 0; i < rows; i++ {
		rowNum := top + i
		if rowNum < 1 || rowNum > height {
			continue
		}
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K", rowNum)
		index := start + i
		if index >= len(layout.Rows) {
			continue
		}
		if index == 0 {
			frame.WriteString(promptStr)
		} else {
			frame.WriteString(indent)
		}
		line := layout.Rows[index].Text
		if line == "" && len(options.InputLine) == 0 && options.Hint != "" {
			line = style.NewStyle().Foreground(options.Theme.Border).Italic(true).Render(style.TruncateCells(options.Hint, promptColumns))
		}
		frame.WriteString(line)
	}
	cursorRow := min(maxPromptRow, max(top, top+layout.CursorRow-start))
	cursorRow = min(height, max(1, cursorRow))
	cursorCol := min(termW, max(1, 1+prefixLen+layout.CursorColumn))
	if options.SetCursor != nil {
		options.SetCursor(cursorRow, cursorCol)
	}
	fmt.Fprintf(&frame, "\x1b[%d;%dH", cursorRow, cursorCol)
	if options.DrawPrompt {
		frame.WriteString(terminal.ShowCursor)
	}
	if sync {
		frame.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(frame.Bytes())
}
