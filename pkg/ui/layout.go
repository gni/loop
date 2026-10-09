package ui

import (
	"fmt"
	"io"
)

// ClearTerminalForStartup clears terminal for clean REPL initialization.
func ClearTerminalForStartup(w io.Writer) {
	clearTerminalForStartup(w)
}

func clearTerminalForStartup(w io.Writer) {
	// Reset any scrolling region left by an earlier process before erasing the
	// complete visible viewport. This preserves scrollback while clearing screen.
	fmt.Fprint(w, "\x1b[r\x1b[2J\x1b[H")
}
