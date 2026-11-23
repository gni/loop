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

// RenderFailure keeps diagnostic details available through history expansion.
func RenderFailure(w io.Writer, message string, collapse bool, theme style.UITheme) {
	renderActionableFailure(w, message, collapse, theme)
}
