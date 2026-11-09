package interactive

import (
	"strings"
	"unicode"

	"loop/pkg/ui/style"
)

// Keep untrusted names and instructions on the rows reserved for the panel.
func menuDisplayText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, style.StripAnsi(text))
}
