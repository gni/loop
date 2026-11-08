package render

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/style"
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
			i = style.SkipAnsiEscape(input, i)
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

// RenderGenerationError renders an error box with rounded borders and styled title.
func RenderGenerationError(w io.Writer, message string, theme style.UITheme) {
	renderActionableFailure(w, message, false, theme)
}

// RenderFailure keeps diagnostic details available through history expansion.
func RenderFailure(w io.Writer, message string, collapse bool, theme style.UITheme) {
	renderActionableFailure(w, message, collapse, theme)
}
