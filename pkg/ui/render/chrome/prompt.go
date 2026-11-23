package chrome

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/style"
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

type promptCursor struct {
	row, column int
}

func promptClusterWidth(cluster string, graphemeWidth, column, columns int) int {
	if cluster == "\t" {
		return min(style.TabAdvance(column, style.DefaultTabWidth), columns)
	}
	first, _ := utf8.DecodeRuneInString(cluster)
	if unicode.IsControl(first) {
		return 0
	}
	return graphemeWidth
}

func (c *promptCursor) prepareCluster(cluster string, graphemeWidth, columns int) (cells int, wrapped bool) {
	if cluster == "\n" {
		return 0, false
	}
	cells = promptClusterWidth(cluster, graphemeWidth, c.column, columns)
	wrapped = c.column > 0 && c.column+cells > columns
	if wrapped {
		c.row++
		c.column = 0
		if cluster == "\t" {
			cells = min(style.TabAdvance(0, style.DefaultTabWidth), columns)
		}
	}
	return cells, wrapped
}

func (c *promptCursor) advanceCluster(cluster string, cells int) {
	if cluster == "\n" {
		c.row++
		c.column = 0
		return
	}
	first, _ := utf8.DecodeRuneInString(cluster)
	if cluster == "\t" || !unicode.IsControl(first) {
		c.column += cells
	}
}

func LayoutPrompt(text string, position, columns int) PromptLayout {
	columns = max(1, columns)
	totalRunes := utf8.RuneCountInString(text)
	position = min(max(0, position), totalRunes)
	layout := PromptLayout{}
	var content strings.Builder
	start := 0
	runeIdx := 0
	cursor := promptCursor{}
	finish := func(end int) {
		layout.Rows = append(layout.Rows, PromptRow{Text: content.String(), Start: start, End: end})
		content.Reset()
	}
	for i := 0; i < len(text); {
		cluster, gw, sz := style.NextGrapheme(text[i:])
		i += sz
		clusterRunes := utf8.RuneCountInString(cluster)
		cells, wrapped := cursor.prepareCluster(cluster, gw, columns)
		if wrapped {
			finish(runeIdx)
			start = runeIdx
		}
		if runeIdx <= position && position < runeIdx+clusterRunes {
			layout.CursorRow = cursor.row
			layout.CursorColumn = cursor.column
		}
		if cluster == "\n" {
			finish(runeIdx)
			runeIdx += clusterRunes
			start = runeIdx
			cursor.advanceCluster(cluster, cells)
			continue
		}
		first, _ := utf8.DecodeRuneInString(cluster)
		switch {
		case cluster == "\t":
			content.WriteString(strings.Repeat(" ", cells))
		case unicode.IsControl(first):
		default:
			content.WriteString(cluster)
		}
		cursor.advanceCluster(cluster, cells)
		runeIdx += clusterRunes
	}
	if position == totalRunes {
		layout.CursorRow = cursor.row
		layout.CursorColumn = cursor.column
	}
	finish(totalRunes)
	return layout
}

// MovePromptVertical moves within the same visual rows used for wrapping.
func MovePromptVertical(text string, position, columns, direction int) (int, bool) {
	columns = max(1, columns)
	layout := LayoutPrompt(text, position, columns)
	target := layout.CursorRow + direction
	if target < 0 || target >= len(layout.Rows) {
		return position, false
	}
	row := layout.Rows[target]
	runes := []rune(text)
	rowText := string(runes[row.Start:row.End])
	col, pos := 0, row.Start
	for i := 0; i < len(rowText); {
		cluster, gw, sz := style.NextGrapheme(rowText[i:])
		cells := promptClusterWidth(cluster, gw, col, columns)
		if col+cells > layout.CursorColumn {
			break
		}
		col += cells
		pos += utf8.RuneCountInString(cluster)
		i += sz
	}
	return pos, true
}
