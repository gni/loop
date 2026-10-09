package style

import (
	"loop/pkg/terminal"
)

// GetTerminalSize probes stdin, stdout, and stderr for terminal dimensions,
// falling back to standard 80x24 if untended or in a pipe.
func GetTerminalSize() (int, int) {
	return terminal.GetDimensions()
}
