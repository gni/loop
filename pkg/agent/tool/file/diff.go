package file

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"loop/pkg/domain/limits"
)

type diffOp int

const (
	diffEqual diffOp = iota
	diffInsert
	diffDelete
)

type diffPart struct {
	op    diffOp
	lines []string
}

func computeMyersDiff(ctx context.Context, a, b []string) ([]diffPart, error) {
	if len(a) == 0 && len(b) == 0 {
		return nil, nil
	}
	if len(a) == 0 {
		return []diffPart{{op: diffInsert, lines: b}}, nil
	}
	if len(b) == 0 {
		return []diffPart{{op: diffDelete, lines: a}}, nil
	}

	prefixLen := 0
	for prefixLen < len(a) && prefixLen < len(b) && a[prefixLen] == b[prefixLen] {
		if prefixLen%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < len(a)-prefixLen && suffixLen < len(b)-prefixLen && a[len(a)-1-suffixLen] == b[len(b)-1-suffixLen] {
		if suffixLen%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		suffixLen++
	}

	var parts []diffPart
	if prefixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[:prefixLen]})
	}

	midA := a[prefixLen : len(a)-suffixLen]
	midB := b[prefixLen : len(b)-suffixLen]
	n := len(midA)
	m := len(midB)

	switch {
	case n == 0 && m == 0:
	case n == 0:
		parts = append(parts, diffPart{op: diffInsert, lines: midB})
	case m == 0:
		parts = append(parts, diffPart{op: diffDelete, lines: midA})
	case n+m > 2000:
		parts = append(parts, diffPart{op: diffDelete, lines: midA}, diffPart{op: diffInsert, lines: midB})
	default:
		maxD := n + m
		offset := maxD
		v := make([]int, 2*maxD+1)
		trace := make([][]int, 0, maxD+1)

		dFound := -1
		for d := 0; d <= maxD; d++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Cap the trace at about 8 MiB on a 64-bit build. Large rewrites
			// fall back to a bounded display of the replaced region.
			if (d+1)*len(v) > 1<<20 {
				break
			}
			vCopy := make([]int, len(v))
			copy(vCopy, v)
			trace = append(trace, vCopy)

			for k := -d; k <= d; k += 2 {
				var x int
				if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
					x = v[k+1+offset]
				} else {
					x = v[k-1+offset] + 1
				}
				y := x - k
				for x < n && y < m && midA[x] == midB[y] {
					x++
					y++
				}
				v[k+offset] = x
				if x >= n && y >= m {
					dFound = d
					break
				}
			}
			if dFound != -1 {
				break
			}
		}

		if dFound == -1 {
			parts = append(parts, diffPart{op: diffDelete, lines: midA}, diffPart{op: diffInsert, lines: midB})
		} else {
			x, y := n, m
			var revParts []diffPart
			addRevLine := func(op diffOp, line string) {
				if len(revParts) > 0 && revParts[len(revParts)-1].op == op {
					revParts[len(revParts)-1].lines = append(revParts[len(revParts)-1].lines, line)
				} else {
					revParts = append(revParts, diffPart{op: op, lines: []string{line}})
				}
			}

			for d := dFound; d > 0; d-- {
				vSnap := trace[d]
				k := x - y
				var prevK int
				if k == -d || (k != d && vSnap[k-1+offset] < vSnap[k+1+offset]) {
					prevK = k + 1
				} else {
					prevK = k - 1
				}
				prevX := vSnap[prevK+offset]
				prevY := prevX - prevK

				for x > prevX && y > prevY && midA[x-1] == midB[y-1] {
					addRevLine(diffEqual, midA[x-1])
					x--
					y--
				}
				if x == prevX {
					addRevLine(diffInsert, midB[y-1])
					y--
				} else {
					addRevLine(diffDelete, midA[x-1])
					x--
				}
			}
			for x > 0 && y > 0 && midA[x-1] == midB[y-1] {
				addRevLine(diffEqual, midA[x-1])
				x--
				y--
			}

			for i := len(revParts) - 1; i >= 0; i-- {
				lines := revParts[i].lines
				for l, r := 0, len(lines)-1; l < r; l, r = l+1, r-1 {
					lines[l], lines[r] = lines[r], lines[l]
				}
				parts = append(parts, revParts[i])
			}
		}
	}

	if suffixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[len(a)-suffixLen:]})
	}

	var merged []diffPart
	for _, p := range parts {
		if len(p.lines) == 0 {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].op == p.op {
			merged[len(merged)-1].lines = append(append([]string(nil), merged[len(merged)-1].lines...), p.lines...)
		} else {
			merged = append(merged, p)
		}
	}
	return merged, nil
}

// GenerateDisplayDiff is the synchronous convenience wrapper.
func GenerateDisplayDiff(oldContent, newContent string, contextLines int) string {
	diff, _ := GenerateDisplayDiffContext(context.Background(), oldContent, newContent, contextLines)
	return diff
}

func diffLines(content string, limit int) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfterN(content, "\n", limit+1)
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// GenerateDisplayDiffContext bounds computation and terminal output separately.
// A truncated display never changes the full replacement applied to the file.
func GenerateDisplayDiffContext(ctx context.Context, oldContent, newContent string, contextLines int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if oldContent == newContent {
		return "", nil
	}
	contextLines = max(0, min(contextLines, 100))
	const maxInputLines = 100000
	oldLines := diffLines(oldContent, maxInputLines)
	newLines := diffLines(newContent, maxInputLines)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(oldLines) > maxInputLines || len(newLines) > maxInputLines {
		return "Diff exceeds display limits.\n", nil
	}
	parts, err := computeMyersDiff(ctx, oldLines, newLines)
	if err != nil {
		return "", err
	}
	width := max(4, len(strconv.Itoa(max(len(oldLines), len(newLines)))))
	const notice = "Diff display truncated.\n"
	var sb strings.Builder
	rows, oldLineNum, newLineNum := 0, 1, 1
	truncated := false
	writeLine := func(op diffOp, line string) bool {
		hasNewline := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\n")
		if len(line) > limits.DefaultReadLineLength {
			line = UTF8SafePrefix(line, limits.DefaultReadLineLength) + "..."
			truncated = true
		}
		var text string
		switch op {
		case diffInsert:
			text = fmt.Sprintf("\x1b[32m%-*d + %s\x1b[0m\n", width, newLineNum, line)
			newLineNum++
		case diffDelete:
			text = fmt.Sprintf("\x1b[31m%-*d - %s\x1b[0m\n", width, oldLineNum, line)
			oldLineNum++
		default:
			text = fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line)
			oldLineNum++
			newLineNum++
		}
		lineCount := 1
		if op != diffEqual && !hasNewline {
			text += "\\ No newline at end of file\n"
			lineCount++
		}
		if rows+lineCount > limits.DefaultToolOutputLines-1 || sb.Len()+len(text)+len(notice) > limits.DefaultToolOutputBytes {
			truncated = true
			return false
		}
		sb.WriteString(text)
		rows += lineCount
		return true
	}
	skipEqual := func(count int) bool {
		oldLineNum += count
		newLineNum += count
		if count == 0 {
			return true
		}
		text := fmt.Sprintf("%-*s   ...\n", width, "")
		if rows >= limits.DefaultToolOutputLines-1 || sb.Len()+len(text)+len(notice) > limits.DefaultToolOutputBytes {
			truncated = true
			return false
		}
		sb.WriteString(text)
		rows++
		return true
	}
display:
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if part.op != diffEqual {
			for _, line := range part.lines {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if !writeLine(part.op, line) {
					break display
				}
			}
			continue
		}
		before, after := 0, 0
		if i > 0 {
			before = min(contextLines, len(part.lines))
		}
		if i < len(parts)-1 {
			after = min(contextLines, len(part.lines)-before)
		}
		for _, line := range part.lines[:before] {
			if !writeLine(diffEqual, line) {
				break display
			}
		}
		if !skipEqual(len(part.lines) - before - after) {
			break display
		}
		for _, line := range part.lines[len(part.lines)-after:] {
			if !writeLine(diffEqual, line) {
				break display
			}
		}
	}
	if truncated {
		sb.WriteString(notice)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return sb.String(), nil
}
