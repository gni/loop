package render

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

type tableGrapheme struct {
	cluster string
	width   int
	isSpace bool
	sgr     style.SGRState
}

// Wrap by terminal cells, restoring inline styles after each table border.
func wrapMarkdownCell(content string, width int) []string {
	width = max(1, width)
	var clusters []tableGrapheme
	var sgr style.SGRState
	for i := 0; i < len(content); {
		if content[i] == 27 {
			end := style.SkipAnsiEscape(content, i)
			sequence := content[i:end]
			if strings.HasPrefix(sequence, "\x1b[") && strings.HasSuffix(sequence, "m") {
				sgr.Apply(sequence)
			}
			i = end
			continue
		}
		cluster, cw, size := style.NextGrapheme(content[i:])
		first, _ := utf8.DecodeRuneInString(cluster)
		if cw > width {
			cluster = "…"
			cw = 1
			first = '…'
		}
		clusters = append(clusters, tableGrapheme{
			cluster: cluster,
			width:   cw,
			isSpace: unicode.IsSpace(first),
			sgr:     sgr,
		})
		i += size
	}
	var lines []string
	for start := 0; start < len(clusters); {
		end, cells, space := start, 0, -1
		for end < len(clusters) && cells+clusters[end].width <= width {
			cells += clusters[end].width
			if clusters[end].isSpace {
				space = end
			}
			end++
		}
		if end == start {
			end = start + 1
		}
		next := end
		if end < len(clusters) && space > start {
			end, next = space, space+1
		}
		var line strings.Builder
		var active style.SGRState
		for _, g := range clusters[start:end] {
			if g.sgr != active {
				if !active.IsZero() {
					line.WriteString(terminal.ResetAttributes)
				}
				if !g.sgr.IsZero() {
					line.WriteString(g.sgr.Sequence())
				}
				active = g.sgr
			}
			line.WriteString(g.cluster)
		}
		if !active.IsZero() {
			line.WriteString(terminal.ResetAttributes)
		}
		lines = append(lines, line.String())
		start = next
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
