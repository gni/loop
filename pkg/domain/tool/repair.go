package tool

import (
	"strings"
)

// RepairJSON attempts to repair malformed JSON parameters produced by language models,
// such as stripping markdown code fences, escaping raw newlines/tabs inside strings,
// and balancing unmatched brackets and braces.
func RepairJSON(js string) string {
	js = strings.TrimSpace(js)
	if js == "" {
		return "{}"
	}

	// 1. Strip markdown code block wrappers if present
	if strings.HasPrefix(js, "```") {
		lines := strings.Split(js, "\n")
		var cleanLines []string
		for _, line := range lines {
			trimmedLine := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmedLine, "```") {
				cleanLines = append(cleanLines, line)
			}
		}
		js = strings.TrimSpace(strings.Join(cleanLines, "\n"))
	}

	// 2. Fix unescaped newlines/tabs inside JSON string values
	var sb strings.Builder
	inString := false
	inEscape := false
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
		if inString && c == '\n' {
			sb.WriteString(`\n`)
		} else if inString && c == '\t' {
			sb.WriteString(`\t`)
		} else if inString && c == '\r' {
			sb.WriteString(`\r`)
		} else {
			sb.WriteByte(c)
		}
	}
	if inString {
		sb.WriteByte('"')
	}
	js = sb.String()

	// 3. Count braces and brackets outside string literals
	openBraces := 0
	closeBraces := 0
	openBrackets := 0
	closeBrackets := 0
	inString = false
	inEscape = false

	for i := 0; i < len(js); i++ {
		c := js[i]
		if inEscape {
			inEscape = false
			continue
		}
		if c == '\\' {
			inEscape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if !inString {
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
		}
	}

	// 4. Fix extra closing braces/brackets at the end
	trimExtraClose := func(s, suffix string, closeCount, openCount *int) string {
		for len(s) > 0 && *closeCount > *openCount && strings.HasSuffix(s, suffix) {
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
			*closeCount--
		}
		return s
	}
	js = trimExtraClose(js, "}", &closeBraces, &openBraces)
	js = trimExtraClose(js, "]", &closeBrackets, &openBrackets)

	// 5. Fix missing closing brackets/braces
	if openBrackets > closeBrackets {
		js += strings.Repeat("]", openBrackets-closeBrackets)
	}
	if openBraces > closeBraces {
		js += strings.Repeat("}", openBraces-closeBraces)
	}

	return js
}
