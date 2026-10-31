package render

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
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

func promptRuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == '\u200d' {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

func LayoutPrompt(text string, position, columns int) PromptLayout {
	columns = max(1, columns)
	runes := []rune(text)
	position = min(max(0, position), len(runes))
	layout := PromptLayout{}
	var content strings.Builder
	start, col := 0, 0
	finish := func(end int) {
		layout.Rows = append(layout.Rows, PromptRow{Text: content.String(), Start: start, End: end})
		content.Reset()
		col = 0
	}
	for i, r := range runes {
		cells := promptRuneWidth(r)
		if r == '\t' {
			cells = min(4-col%4, columns)
		}
		if r != '\n' && col > 0 && col+cells > columns {
			finish(i)
			start = i
		}
		if i == position {
			layout.CursorRow = len(layout.Rows)
			layout.CursorColumn = col
		}
		if r == '\n' {
			finish(i)
			start = i + 1
			continue
		}
		switch {
		case r == '\t':
			content.WriteString(strings.Repeat(" ", cells))
			col += cells
		case unicode.IsControl(r):
		default:
			content.WriteRune(r)
			col += cells
		}
	}
	if position == len(runes) {
		layout.CursorRow = len(layout.Rows)
		layout.CursorColumn = col
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
		cells := promptRuneWidth(runes[pos])
		if runes[pos] == '\t' {
			cells = min(4-col%4, max(1, columns))
		}
		if col+cells > layout.CursorColumn {
			break
		}
		col += cells
		pos++
	}
	return pos, true
}
