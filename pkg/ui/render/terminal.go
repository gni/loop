package render

import (
	"fmt"
	"io"

	"loop/pkg/terminal"
)

func ClearTerminalForStartup(w io.Writer) {
	fmt.Fprint(w, terminal.DisableMouseTracking+"\x1b[r\x1b[2J\x1b[H")
}
