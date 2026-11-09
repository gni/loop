package render

import (
	"strconv"
	"strings"

	"loop/pkg/ui/style"
)

// The first complete row supplies a bounded sample. Once displayed, columns
// retain their widths so later values can wrap without moving earlier rows.
func (t *markdownTable) measure(first []string) {
	count := len(t.header)
	t.preferred, t.minimum, t.numeric = make([]int, count), make([]int, count), make([]bool, count)
	for i, label := range t.header {
		headerWidth := style.CellWidth(RenderInlineMarkdown(label, false, t.theme))
		value := ""
		if i < len(first) {
			value = style.StripAnsi(RenderInlineMarkdown(SanitizeTerminalText(first[i]), false, t.theme))
		}
		number := strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(value), "%"), ",", "")
		_, err := strconv.ParseFloat(number, 64)
		t.numeric[i] = value == "" || err == nil
		preferred := max(1, headerWidth, style.CellWidth(value))
		if !t.numeric[i] {
			// Give text modest room for later rows without stretching the table.
			preferred = max(12, preferred)
		}
		t.preferred[i] = min(40, preferred)
		t.minimum[i] = min(6, t.preferred[i])
	}
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
