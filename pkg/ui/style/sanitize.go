package style

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeTerminalText removes cursor-moving and other control sequences from
// untrusted text before it enters the terminal renderer.
func SanitizeTerminalText(input string) string {
	input = strings.ToValidUTF8(input, "")

	var output strings.Builder
	output.Grow(len(input))

	for i := 0; i < len(input); {
		switch input[i] {
		case '\r':
			if i+1 < len(input) && input[i+1] == '\n' {
				i++
			}
			output.WriteByte('\n')
			i++
		case '\n':
			output.WriteByte('\n')
			i++
		case '\t':
			output.WriteString("    ")
			i++
		case '\x1b':
			i = SkipAnsiEscape(input, i)
		default:
			r, size := utf8.DecodeRuneInString(input[i:])
			i += size
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
				continue
			}
			output.WriteRune(r)
		}
	}

	return strings.TrimRight(output.String(), "\n")
}

// PadRightCells appends spaces to text until its visible terminal cell width reaches width.
func PadRightCells(text string, width int) string {
	pad := width - CellWidth(text)
	if pad <= 0 {
		return text
	}
	return text + strings.Repeat(" ", pad)
}
