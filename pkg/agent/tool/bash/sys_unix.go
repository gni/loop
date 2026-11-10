//go:build !windows

package bash

import (
	"fmt"
	"os/exec"
	"syscall"
)

func extractSignalDiagnostic(exitErr *exec.ExitError) string {
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sig := ws.Signal()
		exitCode := exitErr.ExitCode()
		switch sig {
		case syscall.SIGKILL:
			return fmt.Sprintf("command terminated by SIGKILL (signal 9 / killed, possibly out of memory, exit status %d)", exitCode)
		case syscall.SIGSEGV:
			return fmt.Sprintf("command terminated by SIGSEGV (signal 11 / segmentation fault, exit status %d)", exitCode)
		case syscall.SIGTERM:
			return fmt.Sprintf("command terminated by SIGTERM (signal 15 / termination request, exit status %d)", exitCode)
		case syscall.SIGINT:
			return fmt.Sprintf("command interrupted by SIGINT (signal 2 / Ctrl+C, exit status %d)", exitCode)
		case syscall.SIGABRT:
			return fmt.Sprintf("command aborted by SIGABRT (signal 6 / abort, exit status %d)", exitCode)
		default:
			return fmt.Sprintf("command terminated by signal %d (%s, exit status %d)", sig, sig.String(), exitCode)
		}
	}
	return ""
}
