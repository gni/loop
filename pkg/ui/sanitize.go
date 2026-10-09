package ui

import (
	"io"

	"loop/pkg/ui/render"
)

// sanitizeTerminalText removes cursor-moving and other control sequences from
// untrusted text before it enters the terminal renderer.
func sanitizeTerminalText(input string) string {
	return render.SanitizeTerminalText(input)
}

func RenderGenerationError(w io.Writer, message string, theme UITheme) {
	render.RenderGenerationError(w, message, theme)
}
