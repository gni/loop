package render

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/terminal"
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
	message = strings.TrimSpace(SanitizeTerminalText(message))
	if message == "" {
		message = "generation failed without an error message"
	}

	termWidth, _ := terminal.GetDimensions()
	if termWidth < 20 {
		termWidth = 20
	}

	boxStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Error).
		Padding(1, 2).
		MaxWidth(termWidth)
	titleStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	bodyStyle := style.NewStyle().Foreground(theme.Text)

	content := fmt.Sprintf("%s\n\n%s", titleStyle.Render("error during generation:"), bodyStyle.Render(message))
	fmt.Fprintln(w, boxStyle.Render(content))
}
