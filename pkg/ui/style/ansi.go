package style

import (
	"strings"
	"unicode/utf8"
)

// SkipAnsiEscape returns the end byte index of the ANSI escape sequence starting
// at start in s. It handles CSI, OSC, DCS, SOS, PM, APC, 3-byte charset/intermediate
// sequences (such as "\x1b(B"), and standard 2-byte escapes.
func SkipAnsiEscape(s string, start int) int {
	if start >= len(s) || s[start] != '\x1b' {
		return start
	}
	i := start + 1
	if i >= len(s) {
		return i
	}

	switch s[i] {
	case '[':
		i++
		for i < len(s) {
			final := s[i] >= 0x40 && s[i] <= 0x7e
			i++
			if final {
				return i
			}
		}
	case ']', 'P', 'X', '^', '_':
		i++
		for i < len(s) {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	default:
		// Consume intermediate bytes (0x20-0x2F, e.g. "\x1b(B", "\x1b#8") plus final byte.
		if s[i] >= 0x20 && s[i] <= 0x2f {
			for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
				i++
			}
			if i < len(s) {
				return i + 1
			}
			return i
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		return i + size
	}
	return i
}

// StripAnsi removes ANSI escape codes from str.
func StripAnsi(str string) string {
	if !strings.ContainsRune(str, '\x1b') {
		return str
	}
	var sb strings.Builder
	sb.Grow(len(str))
	for i := 0; i < len(str); {
		if str[i] == '\x1b' {
			i = SkipAnsiEscape(str, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(str[i:])
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}

// TruncateRunes safely truncates a string to maxRunes without slicing multi-byte UTF-8 runes.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// CellWidth computes the terminal column width of text without heap-allocating a stripped copy.
// ANSI escape sequences are skipped, and grapheme clusters (combining marks, emoji ZWJ, VS16)
// are measured accurately.
func CellWidth(text string) int {
	n := 0
	for i := 0; i < len(text); {
		if text[i] == '\x1b' {
			i = SkipAnsiEscape(text, i)
			continue
		}
		b := text[i]
		if b < utf8.RuneSelf {
			if b >= 0x20 && b != 0x7f {
				n++
			}
			i++
			continue
		}
		_, w, sz := NextGrapheme(text[i:])
		n += w
		i += sz
	}
	return n
}

// TruncateCells truncates text to at most limit terminal columns while preserving
// ANSI SGR escapes and never splitting grapheme clusters.
func TruncateCells(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if CellWidth(text) <= limit {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	n := 0
	hasEscape := false
	for i := 0; i < len(text); {
		if text[i] == '\x1b' {
			end := SkipAnsiEscape(text, i)
			out.WriteString(text[i:end])
			hasEscape = true
			i = end
			continue
		}
		cluster, cells, sz := NextGrapheme(text[i:])
		if n+cells > limit-1 {
			break
		}
		out.WriteString(cluster)
		n += cells
		i += sz
	}
	out.WriteRune('…')
	if hasEscape {
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// WrapAnsiLine wraps a single ANSI-styled line to at most limit display columns,
// preserving trailing escapes, carrying active SGR state across line breaks,
// and never splitting grapheme clusters.
func WrapAnsiLine(line string, limit int) []string {
	return wrapSingleAnsiLine(line, limit)
}

// HardWrapAnsiLine splits a single ANSI-styled line at exact terminal display
// column boundaries (matching terminal auto-wrap 1:1) while preserving active
// SGR attributes across wrapped rows and never splitting grapheme clusters.
func HardWrapAnsiLine(line string, limit int) []string {
	if limit <= 0 {
		return []string{line}
	}
	var rows []string
	var cur strings.Builder
	var activeSGR SGRState
	col := 0

	flushRow := func() {
		if !activeSGR.IsZero() {
			cur.WriteString("\x1b[0m")
		}
		rows = append(rows, cur.String())
		cur.Reset()
		if !activeSGR.IsZero() {
			cur.WriteString(activeSGR.Sequence())
		}
		col = 0
	}

	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			end := SkipAnsiEscape(line, i)
			seq := line[i:end]
			cur.WriteString(seq)
			activeSGR.Apply(seq)
			i = end
			continue
		}
		cluster, cw, sz := NextGrapheme(line[i:])
		if cw > 0 && col > 0 && col+cw > limit {
			flushRow()
		}
		cur.WriteString(cluster)
		col += cw
		i += sz
	}

	if cur.Len() > 0 || len(rows) == 0 {
		if !activeSGR.IsZero() {
			cur.WriteString("\x1b[0m")
		}
		rows = append(rows, cur.String())
	}
	return rows
}

func wrapSingleAnsiLine(line string, limit int) []string {
	if limit <= 0 {
		return []string{line}
	}
	if line == "" {
		return []string{""}
	}

	var lines []string
	var curLine strings.Builder
	curLineVisible := 0

	var curWord strings.Builder
	curWordVisible := 0
	var wordStartSGR SGRState
	var activeSGR SGRState
	hasWordSGR := false

	var spaces strings.Builder
	spacesVisible := 0

	emitLine := func() {
		if !activeSGR.IsZero() {
			curLine.WriteString("\x1b[0m")
		}
		lines = append(lines, curLine.String())
		curLine.Reset()
		curLineVisible = 0
	}

	startNextLine := func(sgr SGRState) {
		if !sgr.IsZero() {
			curLine.WriteString(sgr.Sequence())
		}
	}

	flushWord := func() {
		wordLen := curWordVisible
		spacesLen := spacesVisible

		if wordLen == 0 {
			if spacesLen > 0 {
				if curLineVisible > 0 && curLineVisible+spacesLen > limit {
					emitLine()
					startNextLine(activeSGR)
				}
				curLine.WriteString(spaces.String())
				curLineVisible += spacesLen
				spaces.Reset()
				spacesVisible = 0
			}
			if curWord.Len() > 0 {
				curLine.WriteString(curWord.String())
				curWord.Reset()
				hasWordSGR = false
			}
			return
		}

		if curLineVisible == 0 {
			if spacesLen > 0 && spacesLen+wordLen <= limit {
				curLine.WriteString(spaces.String())
				curLineVisible += spacesLen
			}
			spaces.Reset()
			spacesVisible = 0
			curLine.WriteString(curWord.String())
			curLineVisible += wordLen
		} else if curLineVisible+spacesLen+wordLen <= limit {
			if spacesLen > 0 {
				curLine.WriteString(spaces.String())
				curLineVisible += spacesLen
				spaces.Reset()
				spacesVisible = 0
			}
			curLine.WriteString(curWord.String())
			curLineVisible += wordLen
		} else {
			emitLine()
			spaces.Reset()
			spacesVisible = 0
			if hasWordSGR {
				startNextLine(wordStartSGR)
			} else {
				startNextLine(activeSGR)
			}
			curLine.WriteString(curWord.String())
			curLineVisible = wordLen
		}

		curWord.Reset()
		curWordVisible = 0
		hasWordSGR = false
	}

	for i := 0; i < len(line); {
		if line[i] == '\x1b' {
			escEnd := SkipAnsiEscape(line, i)
			seq := line[i:escEnd]
			if curWordVisible == 0 && spacesVisible > 0 {
				// Keep ANSI escapes in exact stream order relative to buffered spaces.
				spaces.WriteString(seq)
			} else {
				if !hasWordSGR && curWordVisible == 0 {
					wordStartSGR = activeSGR
					hasWordSGR = true
				}
				curWord.WriteString(seq)
			}
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				activeSGR.Apply(seq)
			}
			i = escEnd
			continue
		}

		if line[i] == ' ' {
			if curWordVisible > 0 || curWord.Len() > 0 {
				flushWord()
			}
			spaces.WriteByte(' ')
			spacesVisible++
			i++
			continue
		}

		cluster, cw, sz := NextGrapheme(line[i:])
		if !hasWordSGR {
			wordStartSGR = activeSGR
			hasWordSGR = true
		}

		// Break oversized single tokens cleanly on grapheme boundaries.
		if curWordVisible > 0 && curWordVisible+cw > limit {
			flushWord()
			wordStartSGR = activeSGR
			hasWordSGR = true
		}

		curWord.WriteString(cluster)
		curWordVisible += cw
		i += sz
	}

	flushWord()
	if curLine.Len() > 0 || len(lines) == 0 {
		lines = append(lines, curLine.String())
	}

	return lines
}
