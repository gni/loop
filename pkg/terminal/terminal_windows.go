//go:build windows

package terminal

import (
	"os"
)

// NotifyResize is a no-op on Windows where SIGWINCH is not a valid signal.
func NotifyResize(ch chan<- os.Signal) {
}
