package ui

import (
	"fmt"
	"io"
)

func ClearTerminalForStartup(w io.Writer) {
	fmt.Fprint(w, "\x1b[r\x1b[2J\x1b[H")
}