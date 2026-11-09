package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

const maxMarkdownTableLineBytes = 32 * 1024
const maxMarkdownTableColumns = 32

type tableAlignment uint8

const (
	tableLeft tableAlignment = iota
	tableCenter
	tableRight
)

// Split before inline parsing: an escaped pipe is cell content, including in code.
func markdownTableCells(line string) ([]string, bool) {
	if len(line) > maxMarkdownTableLineBytes {
		return nil, false
	}
	line = strings.TrimSpace(line)
	var cells []string
	var cell strings.Builder
	pipes := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) {
			if line[i+1] == '|' {
				cell.WriteByte('|')
				i++
				continue
			}
			cell.WriteByte(line[i])
			i++
			cell.WriteByte(line[i])
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			pipes++
		} else {
			cell.WriteByte(line[i])
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	if strings.HasPrefix(line, "|") {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" && strings.HasSuffix(line, "|") {
		cells = cells[:len(cells)-1]
	}
	return cells, pipes > 0 && len(cells) > 0 && len(cells) <= maxMarkdownTableColumns
}

func markdownTableHeader(header, delimiter string) ([]string, []tableAlignment, bool) {
	cells, ok := markdownTableCells(header)
	if !ok || markdownTableBreak(header) {
		return nil, nil, false
	}
	separators, _ := markdownTableCells(delimiter)
	if len(cells) != len(separators) {
		return nil, nil, false
	}
	align := make([]tableAlignment, len(cells))
	for i, separator := range separators {
		left, right := strings.HasPrefix(separator, ":"), strings.HasSuffix(separator, ":")
		dashes := strings.TrimSuffix(strings.TrimPrefix(separator, ":"), ":")
		if dashes == "" || strings.Trim(dashes, "-") != "" {
			return nil, nil, false
		}
		if left && right {
			align[i] = tableCenter
		} else if right {
			align[i] = tableRight
		}
	}
	return cells, align, true
}

func markdownTableBreak(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
		return true
	}
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") || strings.HasPrefix(trimmed, ">") {
		return true
	}
	if _, _, bullet := extractBulletPrefix(trimmed); bullet {
		return true
	}
	if numbered, _, _ := IsNumberedList(trimmed); numbered {
		return true
	}
	if strings.HasPrefix(trimmed, "#") {
		end := strings.IndexFunc(trimmed, func(ch rune) bool { return ch != '#' })
		if end > 0 && end <= 6 && trimmed[end] == ' ' {
			return true
		}
	}
	for _, marker := range []string{"-", "*", "_"} {
		compact := strings.ReplaceAll(trimmed, " ", "")
		if len(compact) >= 3 && strings.Trim(compact, marker) == "" {
			return true
		}
	}
	return false
}

func markdownTableRow(line string) ([]string, bool) {
	if markdownTableBreak(line) || len(line) > maxMarkdownTableLineBytes {
		return nil, false
	}
	cells, piped := markdownTableCells(line)
	return cells, piped || len(cells) == 1
}

func markdownWidth(w io.Writer) int {
	for depth := 0; w != nil && depth < 16; depth++ {
		if sized, ok := w.(interface{ MarkdownWidth() int }); ok {
			return max(1, sized.MarkdownWidth()-1)
		}
		if wrapped, ok := w.(interface{ Unwrap() io.Writer }); ok {
			w = wrapped.Unwrap()
		} else {
			break
		}
	}
	width, _ := terminal.GetDimensions()
	return max(1, width-1)
}

type markdownTable struct {
	header    []string
	align     []tableAlignment
	widths    []int
	preferred []int
	minimum   []int
	numeric   []bool
	width     int
	stacked   bool
	started   bool
	rows      int
	theme     style.UITheme
}

func newMarkdownTable(w io.Writer, header []string, align []tableAlignment, theme style.UITheme) *markdownTable {
	for i := range header {
		header[i] = SanitizeTerminalText(header[i])
	}
	return &markdownTable{header: header, align: align, width: markdownWidth(w), theme: theme}
}

func (t *markdownTable) border(left, middle, right string) string {
	parts := make([]string, len(t.widths))
	for i, width := range t.widths {
		parts[i] = strings.Repeat("─", width+2)
	}
	return style.NewStyle().Foreground(t.theme.Border).Render(left + strings.Join(parts, middle) + right)
}

func (t *markdownTable) start(w io.Writer, first []string) {
	t.measure(first)
	t.layout(markdownWidth(w))
	t.started = true
	if t.stacked {
		heading := renderInlineMarkdownBlock(strings.Join(t.header, " · "), false, t.theme, MarkdownHeading, 2)
		fmt.Fprint(w, strings.Join(wrapMarkdownCell(heading, t.width), "\n"))
		return
	}
	fmt.Fprint(w, t.border("╭", "┬", "╮"), "\n")
	t.writeCells(w, t.header, true)
	fmt.Fprint(w, "\n", t.border("├", "┼", "┤"))
}

func (t *markdownTable) writeRow(w io.Writer, cells []string) {
	if !t.started {
		t.start(w, cells)
	} else {
		t.reflow(w)
	}
	if t.stacked {
		fmt.Fprint(w, "\n", style.NewStyle().Foreground(t.theme.Border).Render(strings.Repeat("─", t.width)))
		for i, label := range t.header {
			value := ""
			if i < len(cells) {
				value = cells[i]
			}
			label = renderInlineMarkdownBlock(label+":", false, t.theme, MarkdownHeading, 2)
			fmt.Fprint(w, "\n", strings.Join(wrapMarkdownCell(label, t.width), "\n"))
			indent := min(2, max(0, t.width-2))
			for _, line := range wrapMarkdownCell(RenderInlineMarkdown(SanitizeTerminalText(value), false, t.theme), t.width-indent) {
				fmt.Fprint(w, "\n", strings.Repeat(" ", indent), line)
			}
		}
	} else {
		if t.rows > 0 {
			fmt.Fprint(w, "\n", t.border("├", "┼", "┤"))
		}
		fmt.Fprint(w, "\n")
		t.writeCells(w, cells, false)
	}
	t.rows++
}

func (t *markdownTable) writeCells(w io.Writer, cells []string, heading bool) {
	columns := make([][]string, len(t.header))
	height := 1
	for i := range columns {
		value := ""
		if i < len(cells) {
			value = SanitizeTerminalText(cells[i])
		}
		formatted := RenderInlineMarkdown(value, false, t.theme)
		if heading {
			formatted = renderInlineMarkdownBlock(value, false, t.theme, MarkdownHeading, 2)
		}
		columns[i] = wrapMarkdownCell(formatted, t.widths[i])
		height = max(height, len(columns[i]))
	}
	bar := style.NewStyle().Foreground(t.theme.Border).Render("│")
	for row := 0; row < height; row++ {
		if row > 0 {
			fmt.Fprint(w, "\n")
		}
		fmt.Fprint(w, bar)
		for i, column := range columns {
			text := ""
			if row < len(column) {
				text = column[row]
			}
			padding := max(0, t.widths[i]-style.CellWidth(text))
			left := 0
			if t.align[i] == tableRight {
				left = padding
			} else if t.align[i] == tableCenter {
				left = padding / 2
			}
			fmt.Fprint(w, " ", strings.Repeat(" ", left), text, strings.Repeat(" ", padding-left), " ", bar)
		}
	}
}

func (t *markdownTable) finish(w io.Writer) {
	if !t.started {
		t.start(w, nil)
	} else {
		t.reflow(w)
	}
	if !t.stacked {
		fmt.Fprint(w, "\n", t.border("╰", "┴", "╯"))
	}
}

func (t *markdownTable) reflow(w io.Writer) {
	if markdownWidth(w) != t.width {
		t.layout(markdownWidth(w))
	}
}
