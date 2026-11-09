package tool

import (
	"fmt"
	"strings"
	"unicode"
)

// SplitCommandArgs groups quoted arguments without evaluating shell syntax.
func SplitCommandArgs(input string) ([]string, error) {
	return parseCommandArgs(input, true)
}

// ParseCommandArgs also accepts unfinished input while expanding prompt text.
func ParseCommandArgs(input string) []string {
	args, _ := parseCommandArgs(input, false)
	return args
}

func parseCommandArgs(input string, strict bool) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\\' && quote != '\'' {
			if i+1 == len(runes) {
				if strict {
					return nil, fmt.Errorf("unfinished escape in command arguments")
				}
				word.WriteRune(ch)
				started = true
				continue
			}
			next := runes[i+1]
			// Preserve path separators unless they escape quoting, whitespace or
			// another backslash. Shell expansions remain literal in every mode.
			escaped := next == '\\' || next == '"' || next == '\'' || unicode.IsSpace(next)
			if quote == '"' {
				escaped = next == '\\' || next == '"' || next == '$' || next == '`' || next == '\n'
			}
			if !escaped {
				word.WriteRune(ch)
				started = true
				continue
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
			continue
		}
		switch {
		case ch == '\'' || ch == '"':
			quote, started = ch, true
		case unicode.IsSpace(ch):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(ch)
			started = true
		}
	}
	if quote != 0 && strict {
		return nil, fmt.Errorf("unclosed quote in command arguments")
	}
	if started {
		args = append(args, word.String())
	}
	return args, nil
}
