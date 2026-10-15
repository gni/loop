package file

import (
	"fmt"
	"strconv"
	"strings"
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

func computeMyersDiff(a, b []string) []diffPart {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	if len(a) == 0 {
		return []diffPart{{op: diffInsert, lines: b}}
	}
	if len(b) == 0 {
		return []diffPart{{op: diffDelete, lines: a}}
	}

	prefixLen := 0
	for prefixLen < len(a) && prefixLen < len(b) && a[prefixLen] == b[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < len(a)-prefixLen && suffixLen < len(b)-prefixLen && a[len(a)-1-suffixLen] == b[len(b)-1-suffixLen] {
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
					revParts[len(revParts)-1].lines = append([]string{line}, revParts[len(revParts)-1].lines...)
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
			merged[len(merged)-1].lines = append(merged[len(merged)-1].lines, p.lines...)
		} else {
			merged = append(merged, p)
		}
	}
	return merged
}

func CountDiffStats(oldContent, newContent string) (added int, deleted int) {
	parts := computeMyersDiff(strings.Split(oldContent, "\n"), strings.Split(newContent, "\n"))
	for _, p := range parts {
		switch p.op {
		case diffInsert:
			added += len(p.lines)
		case diffDelete:
			deleted += len(p.lines)
		}
	}
	return added, deleted
}

func GenerateDisplayDiff(oldContent, newContent string, contextLines int) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	parts := computeMyersDiff(oldLines, newLines)
	if len(parts) == 0 {
		return ""
	}

	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	width := len(strconv.Itoa(maxLineNum))
	if width < 4 {
		width = 4
	}

	var sb strings.Builder
	oldLineNum := 1
	newLineNum := 1
	lastWasChange := false

	writeEqual := func(lines []string) {
		for _, line := range lines {
			sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
			oldLineNum++
			newLineNum++
		}
	}
	skipEqual := func(count int) {
		if count > 0 {
			sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
			oldLineNum += count
			newLineNum += count
		}
	}

	for i, part := range parts {
		if part.op == diffInsert || part.op == diffDelete {
			for _, line := range part.lines {
				if part.op == diffInsert {
					sb.WriteString(fmt.Sprintf("\x1b[32m%-*d + %s\x1b[0m\n", width, newLineNum, line))
					newLineNum++
				} else {
					sb.WriteString(fmt.Sprintf("\x1b[31m%-*d - %s\x1b[0m\n", width, oldLineNum, line))
					oldLineNum++
				}
			}
			lastWasChange = true
			continue
		}

		raw := part.lines
		hasLeadingChange := lastWasChange
		hasTrailingChange := i < len(parts)-1 && (parts[i+1].op == diffInsert || parts[i+1].op == diffDelete)

		switch {
		case hasLeadingChange && hasTrailingChange:
			if len(raw) <= contextLines*2 {
				writeEqual(raw)
			} else {
				writeEqual(raw[:contextLines])
				skipEqual(len(raw) - contextLines*2)
				writeEqual(raw[len(raw)-contextLines:])
			}
		case hasLeadingChange:
			shown := raw
			if len(shown) > contextLines {
				shown = raw[:contextLines]
			}
			writeEqual(shown)
			skipEqual(len(raw) - len(shown))
		case hasTrailingChange:
			skipped := len(raw) - contextLines
			if skipped < 0 {
				skipped = 0
			}
			skipEqual(skipped)
			writeEqual(raw[skipped:])
		default:
			oldLineNum += len(raw)
			newLineNum += len(raw)
		}
		lastWasChange = false
	}

	return sb.String()
}