package tool

import (
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

// RepairJSON repairs malformed JSON parameters produced by language models,
// stripping markdown code fences, escaping raw newlines/tabs inside strings,
// removing trailing commas before closing delimiters, and balancing unmatched
// brackets and braces.
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
	}

	var sb strings.Builder
	sb.Grow(len(js) + 8)
	inString := false
	inEscape := false
	openBraces, closeBraces := 0, 0
	openBrackets, closeBrackets := 0, 0

	for i := 0; i < len(js); i++ {
		c := js[i]
		if inEscape {
			sb.WriteByte(c)
			inEscape = false
			continue
		}
		if c == '\\' {
			sb.WriteByte(c)
			inEscape = true
			continue
		}
		if c == '"' {
			inString = !inString
			sb.WriteByte(c)
			continue
		}
		if inString {
			switch c {
			case '\n':
				sb.WriteString(`\n`)
			case '\t':
				sb.WriteString(`\t`)
			case '\r':
				sb.WriteString(`\r`)
			default:
				sb.WriteByte(c)
			}
			continue
		}

		if c == ',' && hasClosingDelimiterAhead(js, i+1) {
			continue
		}

		switch c {
		case '{':
			openBraces++
		case '}':
			closeBraces++
		case '[':
			openBrackets++
		case ']':
			closeBrackets++
		}
		sb.WriteByte(c)
	}

	if inString {
		sb.WriteByte('"')
	}
	js = sb.String()

	trimExtraClose := func(s, suffix string, closeCount, openCount *int) string {
		for len(s) > 0 && *closeCount > *openCount && strings.HasSuffix(s, suffix) {
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
			*closeCount--
		}
		return s
	}
	js = trimExtraClose(js, "}", &closeBraces, &openBraces)
	js = trimExtraClose(js, "]", &closeBrackets, &openBrackets)

	if openBrackets > closeBrackets {
		js += strings.Repeat("]", openBrackets-closeBrackets)
	}
	if openBraces > closeBraces {
		js += strings.Repeat("}", openBraces-closeBraces)
	}

	return js
}