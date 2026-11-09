package bash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NewCommand requests unbuffered child output where available. Programs that
// manage their own buffering still need to emit bytes before Loop can show them.
func NewCommand(command string) *exec.Cmd {
	executable, args := "bash", []string{"-c", command}
	if stdbuf, err := exec.LookPath("stdbuf"); err == nil {
		executable = stdbuf
		args = []string{"-o0", "-e0", "bash", "-c", command}
	}
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8", "PYTHONUNBUFFERED=1")
	setupSysProcAttr(cmd)
	return cmd
}

// ShellQuote protects paths with spaces, quotes, or shell metacharacters.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// FormatExitError formats an execution failure with signal and status details.
func FormatExitError(err error, timeoutCtx context.Context) string {
	return FormatExitErrorWithTimeout(err, timeoutCtx, 120*time.Second)
}

// FormatExitErrorWithTimeout formats an execution failure using the configured timeout.
func FormatExitErrorWithTimeout(err error, timeoutCtx context.Context, timeout time.Duration) string {
	if timeoutCtx.Err() == context.DeadlineExceeded {
		secs := max(1, int(timeout.Seconds()))
		return fmt.Sprintf("command timed out after %d seconds. If this is a server or long-running process, use 'background: true'", secs)
	} else if timeoutCtx.Err() == context.Canceled {
		return "command cancelled by user"
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Sprintf("command failed: %v", err)
	}

	exitCode := exitErr.ExitCode()
	if diag := extractSignalDiagnostic(exitErr); diag != "" {
		return diag
	}

	switch exitCode {
	case 127:
		return "command not found: exit status 127"
	case 126:
		return "command cannot execute: exit status 126 (permission denied)"
	default:
		return fmt.Sprintf("command failed: exit status %d", exitCode)
	}
}
