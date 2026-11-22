package markdown

import (
	"strconv"
	"strings"

	"loop/pkg/ui/style"
)

// measure uses the first complete row as a bounded sample while streaming.
// Once displayed, columns retain their widths so earlier rows never shift.
func (t *markdownTable) measure(first []string) {
	t.measureRowsWithHeuristic([][]string{first}, true)
}

// measureRows measures all rows in batch mode so every column gets optimal width.
func (t *markdownTable) measureRows(rows [][]string) {
	t.measureRowsWithHeuristic(rows, false)
}

func (t *markdownTable) measureRowsWithHeuristic(rows [][]string, streamingSample bool) {
	count := len(t.header)
	t.preferred, t.minimum, t.numeric = make([]int, count), make([]int, count), make([]bool, count)
	for i, label := range t.header {
		headerWidth := style.CellWidth(RenderInlineMarkdown(label, t.thinking, t.theme))
		allNumeric := true
		hasValue := false
		maxValWidth := 0
		for _, row := range rows {
			if i >= len(row) {
				continue
			}
			value := style.StripAnsi(RenderInlineMarkdown(style.SanitizeTerminalText(row[i]), t.thinking, t.theme))
			if value == "" {
				continue
			}
			hasValue = true
			if w := style.CellWidth(value); w > maxValWidth {
				maxValWidth = w
			}
			number := strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(value), "%"), ",", "")
			if _, err := strconv.ParseFloat(number, 64); err != nil {
				allNumeric = false
			}
		}
		t.numeric[i] = !hasValue || allNumeric
		preferred := max(1, headerWidth, maxValWidth)
		if !t.numeric[i] && streamingSample {
			// Give text modest room for later rows without stretching the table.
			preferred = max(12, preferred)
		}
		t.preferred[i] = min(40, preferred)
		t.minimum[i] = min(6, t.preferred[i])
	}
	t.measured = true
}

func (t *markdownTable) layout(width int) {
	t.width = width
	budget := width - 3*len(t.header) - 1
	minimum, used := 0, 0
	for i := range t.preferred {
		minimum += t.minimum[i]
		used += t.preferred[i]
	}
	t.stacked = minimum > budget
	t.widths = nil
	if t.stacked {
		return
	}
	t.widths = append(t.widths, t.preferred...)
	for used > budget {
		column, best := -1, 0
		for i, current := range t.widths {
			slack := current - t.minimum[i]
			if slack <= 0 {
				continue
			}
			score := slack
			if !t.numeric[i] {
				score += 40
			}
			if score > best {
				column, best = i, score
			}
		}
		if column < 0 {
			break
		}
		t.widths[column]--
		used--
	}
}
