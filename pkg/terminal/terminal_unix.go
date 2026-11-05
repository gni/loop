//go:build !windows

package terminal

import (
	"os"
	"os/signal"
	"syscall"
)

// NotifyResize registers ch to receive terminal window resize signals on Unix systems.
func NotifyResize(ch chan<- os.Signal) {
	if ch != nil {
		signal.Notify(ch, syscall.SIGWINCH)
	}
}
