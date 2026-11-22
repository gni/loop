package render

import (
	"io"

	"loop/pkg/ui/style"
)

// SanitizeTerminalText removes cursor-moving and other control sequences from
// untrusted text before it enters the terminal renderer.
func SanitizeTerminalText(input string) string {
	return style.SanitizeTerminalText(input)
}

// RenderGenerationError renders an error box with rounded borders and styled title.
func RenderGenerationError(w io.Writer, message string, theme style.UITheme) {
	renderActionableFailure(w, message, false, theme)
}

// RenderFailure keeps diagnostic details available through history expansion.
func RenderFailure(w io.Writer, message string, collapse bool, theme style.UITheme) {
	renderActionableFailure(w, message, collapse, theme)
}
