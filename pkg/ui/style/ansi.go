package style

import (
	"strings"
	"unicode/utf8"
)

// SkipAnsiEscape returns the end index of the ANSI escape sequence starting at start in s.
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
		_, size := utf8.DecodeRuneInString(s[i:])
		return i + size
	}
	return i
}

// StripAnsi removes ANSI escape codes from str.
func StripAnsi(str string) string {
	var sb strings.Builder
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

	var spaces strings.Builder

	flushWord := func() {
		wordLen := curWordVisible
		spacesLen := spaces.Len()

		if wordLen == 0 {
			if spacesLen > 0 {
				if curLineVisible+spacesLen <= limit {
					curLine.WriteString(spaces.String())
					curLineVisible += spacesLen
				} else {
					if curLineVisible == 0 {
						curLine.WriteString(spaces.String())
						curLineVisible += spacesLen
					} else {
						lines = append(lines, curLine.String())
						curLine.Reset()
						curLine.WriteString(spaces.String())
						curLineVisible = spacesLen
					}
				}
				spaces.Reset()
			}
			return
		}

		if curLineVisible == 0 {
			if spacesLen > 0 {
				curLine.WriteString(spaces.String())
				curLineVisible += spacesLen
				spaces.Reset()
			}
			curLine.WriteString(curWord.String())
			curLineVisible += wordLen
		} else {
			needed := spacesLen + wordLen
			if curLineVisible+needed <= limit {
				curLine.WriteString(spaces.String())
				curLine.WriteString(curWord.String())
				curLineVisible += needed
				spaces.Reset()
			} else {
				lines = append(lines, curLine.String())
				curLine.Reset()
				curLine.WriteString(curWord.String())
				curLineVisible = wordLen
				spaces.Reset()
			}
		}
		curWord.Reset()
		curWordVisible = 0
	}

	i := 0
	for i < len(line) {
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == '\x1b' {
			escEnd := SkipAnsiEscape(line, i)
			curWord.WriteString(line[i:escEnd])
			i = escEnd
			continue
		}

		if r == ' ' {
			if curWordVisible > 0 {
				flushWord()
			}
			spaces.WriteRune(' ')
			i += size
			continue
		}

		if curWordVisible >= limit {
			flushWord()
		}

		curWord.WriteRune(r)
		curWordVisible++
		i += size
	}

	flushWord()
	if curLineVisible > 0 || len(lines) == 0 {
		lines = append(lines, curLine.String())
	}

	return lines
}
