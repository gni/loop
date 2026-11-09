package render

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

type tableRune struct {
	rune rune
	sgr  string
}

// Wrap by terminal cells, restoring inline styles after each table border.
func wrapMarkdownCell(content string, width int) []string {
	width = max(1, width)
	var chars []tableRune
	sgr := ""
	for i := 0; i < len(content); {
		if content[i] == 27 {
			end := style.SkipAnsiEscape(content, i)
			sequence := content[i:end]
			if sequence == "\x1b[0m" {
				sgr = ""
			} else if strings.HasSuffix(sequence, "m") {
				sgr += sequence
			}
			i = end
			continue
		}
		ch, size := utf8.DecodeRuneInString(content[i:])
		if style.RuneWidth(ch) > width {
			ch = '…'
		}
		chars = append(chars, tableRune{ch, sgr})
		i += size
	}
	var lines []string
	for start := 0; start < len(chars); {
		end, cells, space := start, 0, -1
		for end < len(chars) && cells+style.RuneWidth(chars[end].rune) <= width {
			cells += style.RuneWidth(chars[end].rune)
			if unicode.IsSpace(chars[end].rune) {
				space = end
			}
			end++
		}
		next := end
		if end < len(chars) && space > start {
			end, next = space, space+1
		}
		var line strings.Builder
		active := ""
		for _, ch := range chars[start:end] {
			if ch.sgr != active {
				if active != "" {
					line.WriteString("\x1b[0m")
				}
				line.WriteString(ch.sgr)
				active = ch.sgr
			}
			line.WriteRune(ch.rune)
		}
		if active != "" {
			line.WriteString("\x1b[0m")
		}
		lines = append(lines, line.String())
		start = next
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
