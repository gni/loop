package file

import (
	"fmt"
	"strconv"
	"strings"

	"loop/pkg/domain/diagnostic"
)

type textMatch struct {
	startByte int
	endByte   int
	newText   string
}

// uniqueIndex includes overlapping occurrences, unlike strings.Count.
func uniqueIndex(content, text string) (int, error) {
	first := strings.Index(content, text)
	if first >= 0 && strings.Contains(content[first+1:], text) {
		return -1, &diagnostic.Error{Code: diagnostic.AmbiguousEdit, Cause: fmt.Errorf("oldText is not unique; include more surrounding text")}
	}
	return first, nil
}

func locateEditMatch(content, oldText, newText string) (*textMatch, error) {
	// Match the LF form shown by read/grep, then translate the matched span
	// back to the original bytes. Untouched line endings must stay untouched.
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	oldText = strings.ReplaceAll(oldText, "\r\n", "\n")
	newText = strings.ReplaceAll(newText, "\r\n", "\n")
	match, err := locateNormalizedEditMatch(normalized, oldText, newText)
	if err != nil || match == nil {
		return match, err
	}
	unchanged := match.newText == normalized[match.startByte:match.endByte]
	for offset := 0; offset < len(content); {
		next := strings.Index(content[offset:], "\r\n")
		if next < 0 {
			break
		}
		next += offset
		if next >= match.endByte {
			break
		}
		if next < match.startByte {
			match.startByte++
		}
		match.endByte++
		offset = next + 2
	}
	if unchanged {
		match.newText = content[match.startByte:match.endByte]
		return match, nil
	}
	// Use the target line's ending for inserted lines. An unterminated final
	// line inherits the preceding line's convention.
	newline := strings.IndexByte(content[match.startByte:], '\n')
	if newline >= 0 {
		newline += match.startByte
	} else {
		newline = strings.LastIndexByte(content[:match.startByte], '\n')
	}
	if newline > 0 && content[newline-1] == '\r' {
		match.newText = strings.ReplaceAll(match.newText, "\n", "\r\n")
	}
	return match, nil
}

func locateNormalizedEditMatch(content, oldText, newText string) (*textMatch, error) {
	if oldText == "" {
		return nil, nil
	}
	start, err := uniqueIndex(content, oldText)
	if err != nil {
		return nil, err
	}
	if start >= 0 {
		return &textMatch{startByte: start, endByte: start + len(oldText), newText: newText}, nil
	}
	// Recognize copied read/grep line numbers only when every numbered line
	// agrees with its actual position and exact contents in the file.
	stripped, line, ok := numberedBlock(oldText)
	if !ok {
		return nil, nil
	}
	start, err = uniqueIndex(content, stripped)
	if err != nil {
		return nil, err
	}
	if start < 0 || (start > 0 && content[start-1] != '\n') || strings.Count(content[:start], "\n")+1 != line {
		return nil, nil
	}
	end := start + len(stripped)
	if end < len(content) && content[end] != '\n' && !strings.HasSuffix(stripped, "\n") {
		return nil, nil
	}
	return &textMatch{startByte: start, endByte: end, newText: newText}, nil
}

func numberedBlock(text string) (string, int, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return "", 0, false
	}
	first := 0
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		n := 0
		for n < len(trimmed) && trimmed[n] >= '0' && trimmed[n] <= '9' {
			n++
		}
		if n == 0 || n >= len(trimmed) || (trimmed[n] != ':' && trimmed[n] != '|') {
			return "", 0, false
		}
		number, err := strconv.Atoi(trimmed[:n])
		if err != nil || number <= 0 {
			return "", 0, false
		}
		if i == 0 {
			first = number
		}
		if number-first != i {
			return "", 0, false
		}
		lines[i] = strings.TrimPrefix(trimmed[n+1:], " ")
	}
	return strings.Join(lines, "\n"), first, true
}
