package render

import (
	"loop/pkg/ui/style"
	"strings"
	"unicode"
)

// PromptRow retains source rune positions so navigation and drawing agree.
type PromptRow struct {
	Text       string
	Start, End int
}

type PromptLayout struct {
	Rows                    []PromptRow
	CursorRow, CursorColumn int
}

func promptRuneWidth(r rune) int { return style.RuneWidth(r) }

type promptCursor struct {
	row, column int
}

func promptCellWidth(r rune, column, columns int) int {
	if r == '\t' {
		return min(4-column%4, columns)
	}
	return promptRuneWidth(r)
}

func (c *promptCursor) prepare(r rune, columns int) (cells int, wrapped bool) {
	cells = promptCellWidth(r, c.column, columns)
	wrapped = r != '\n' && c.column > 0 && c.column+cells > columns
	if wrapped {
		c.row++
		c.column = 0
	}
	return cells, wrapped
}

func (c *promptCursor) advance(r rune, cells int) {
	if r == '\n' {
		c.row++
		c.column = 0
	} else if r == '\t' || !unicode.IsControl(r) {
		c.column += cells
	}
}

func (c *promptCursor) advanceText(text string, columns int) {
	for _, r := range text {
		cells, _ := c.prepare(r, columns)
		c.advance(r, cells)
	}
}

func LayoutPrompt(text string, position, columns int) PromptLayout {
	columns = max(1, columns)
	runes := []rune(text)
	position = min(max(0, position), len(runes))
	layout := PromptLayout{}
	var content strings.Builder
	start := 0
	cursor := promptCursor{}
	finish := func(end int) {
		layout.Rows = append(layout.Rows, PromptRow{Text: content.String(), Start: start, End: end})
		content.Reset()
	}
	for i, r := range runes {
		cells, wrapped := cursor.prepare(r, columns)
		if wrapped {
			finish(i)
			start = i
		}
		if i == position {
			layout.CursorRow = cursor.row
			layout.CursorColumn = cursor.column
		}
		if r == '\n' {
			finish(i)
			start = i + 1
			cursor.advance(r, cells)
			continue
		}
		switch {
		case r == '\t':
			content.WriteString(strings.Repeat(" ", cells))
		case unicode.IsControl(r):
		default:
			content.WriteRune(r)
		}
		cursor.advance(r, cells)
	}
	if position == len(runes) {
		layout.CursorRow = cursor.row
		layout.CursorColumn = cursor.column
	}
	finish(len(runes))
	return layout
}

// MovePromptVertical moves within the same visual rows used for wrapping.
func MovePromptVertical(text string, position, columns, direction int) (int, bool) {
	layout := LayoutPrompt(text, position, columns)
	target := layout.CursorRow + direction
	if target < 0 || target >= len(layout.Rows) {
		return position, false
	}
	row := layout.Rows[target]
	runes := []rune(text)
	col, pos := 0, row.Start
	for pos < row.End {
		cells := promptCellWidth(runes[pos], col, max(1, columns))
		if col+cells > layout.CursorColumn {
			break
		}
		col += cells
		pos++
	}
	return pos, true
}
