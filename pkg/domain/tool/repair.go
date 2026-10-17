package tool

import (
	"fmt"
	"strings"
)

func hasClosingDelimiterAhead(s string, idx int) bool {
	for j := idx; j < len(s); j++ {
		switch s[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case '}', ']':
			return true
		default:
			return false
		}
	}
	return false
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hasFourHexDigits(s string, idx int) bool {
	if idx+4 > len(s) {
		return false
	}
	for i := 0; i < 4; i++ {
		if !isHexByte(s[idx+i]) {
			return false
		}
	}
	return true
}

// RepairJSON repairs malformed JSON parameters produced by language models,
// stripping markdown code fences, escaping raw newlines/tabs and invalid
// backslash escapes inside strings, removing trailing commas before closing
// delimiters, and balancing unmatched brackets and braces in LIFO order.
func RepairJSON(js string) string {
	js = strings.TrimSpace(js)
	if js == "" {
		return "{}"
	}

	if strings.HasPrefix(js, "```") {
		lines := strings.Split(js, "\n")
		cleanLines := make([]string, 0, len(lines))
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "```") {
				cleanLines = append(cleanLines, line)
			}
		}
		js = strings.TrimSpace(strings.Join(cleanLines, "\n"))
		if js == "" {
			return "{}"
		}
	}

	var sb strings.Builder
	sb.Grow(len(js) + 16)
	inString := false
	inEscape := false
	var stack []byte

	for i := 0; i < len(js); i++ {
		c := js[i]
		if inString {
			if inEscape {
				inEscape = false
				switch c {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					sb.WriteByte('\\')
					sb.WriteByte(c)
				case 'u':
					if hasFourHexDigits(js, i+1) {
						sb.WriteString(`\u`)
					} else {
						sb.WriteString(`\\u`)
					}
				case '\n':
					sb.WriteString(`\n`)
				case '\r':
					sb.WriteString(`\r`)
				case '\t':
					sb.WriteString(`\t`)
				default:
					sb.WriteString(`\\`)
					sb.WriteByte(c)
				}
				continue
			}
			if c == '\\' {
				inEscape = true
				continue
			}
			if c == '"' {
				inString = false
				sb.WriteByte(c)
				continue
			}
			switch {
			case c == '\n':
				sb.WriteString(`\n`)
			case c == '\t':
				sb.WriteString(`\t`)
			case c == '\r':
				sb.WriteString(`\r`)
			case c < 0x20:
				fmt.Fprintf(&sb, `\u%04x`, c)
			default:
				sb.WriteByte(c)
			}
			continue
		}

		if c == '"' {
			inString = true
			sb.WriteByte(c)
			continue
		}

		if c == ',' && hasClosingDelimiterAhead(js, i+1) {
			continue
		}

		switch c {
		case '{':
			stack = append(stack, '}')
			sb.WriteByte(c)
		case '[':
			stack = append(stack, ']')
			sb.WriteByte(c)
		case '}', ']':
			if len(stack) == 0 {
				continue
			}
			if stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
				sb.WriteByte(c)
			} else if len(stack) >= 2 && stack[len(stack)-2] == c {
				sb.WriteByte(stack[len(stack)-1])
				sb.WriteByte(c)
				stack = stack[:len(stack)-2]
			}
		default:
			sb.WriteByte(c)
		}
	}

	if inString {
		if inEscape {
			sb.WriteString(`\\`)
		}
		sb.WriteByte('"')
	}

	for i := len(stack) - 1; i >= 0; i-- {
		sb.WriteByte(stack[i])
	}

	return sb.String()
}