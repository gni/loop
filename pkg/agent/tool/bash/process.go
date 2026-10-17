package bash

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf8"
)

// ShellQuote protects paths with spaces, quotes, or shell metacharacters.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type cappedOutput struct {
	buf bytes.Buffer
	limit int
	overflow bool
}

func newCappedOutput(limit int) *cappedOutput {
	return &cappedOutput{limit: limit}
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		if original > 0 { c.overflow = true }
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		c.overflow = true
	}
	_, _ = c.buf.Write(p)
	return original, nil
}

func (c *cappedOutput) String() string { return c.buf.String() }

// CleanBackgroundCommand strips nohup, background ampersand, and trailing echo syntax.
func CleanBackgroundCommand(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	if idx := strings.Index(trimmed, "& echo"); idx != -1 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	trimmed = strings.TrimSuffix(trimmed, "&")
	trimmed = strings.TrimSpace(trimmed)
	if strings.HasPrefix(trimmed, "nohup ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "nohup "))
	}
	trimmed = strings.ReplaceAll(trimmed, "&& nohup ", "&& ")
	trimmed = strings.ReplaceAll(trimmed, "; nohup ", "; ")
	return trimmed
}

// FormatExitError formats an execution failure with signal and status details.
func FormatExitError(err error, timeoutCtx context.Context) string {
	if timeoutCtx.Err() == context.DeadlineExceeded {
		return "command timed out after 120 seconds. If this is a server or long-running process, use 'background: true'"
	} else if timeoutCtx.Err() == context.Canceled {
		return "command cancelled by user"
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return fmt.Sprintf("command failed: %v", err)
	}

	exitCode := exitErr.ExitCode()
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sig := ws.Signal()
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

	switch exitCode {
	case 127:
		return "command not found: exit status 127"
	case 126:
		return "command cannot execute: exit status 126 (permission denied)"
	default:
		return fmt.Sprintf("command failed: exit status %d", exitCode)
	}
}

// TruncateOutput bounds the lines and byte volume of terminal output to protect context limits.
func TruncateOutput(combined string) string {
	lines := strings.Split(combined, "\n")
	if len(lines) > 200 {
		var truncatedLines []string
		truncatedLines = append(truncatedLines, lines[:20]...)
		truncatedLines = append(truncatedLines, fmt.Sprintf("\n... [ %d lines omitted to save context length ] ...\n", len(lines)-100))
		truncatedLines = append(truncatedLines, lines[len(lines)-80:]...)
		combined = strings.Join(truncatedLines, "\n")
	}

	if len(combined) > 50000 {
		left := 25000
		for left < len(combined) && !utf8.RuneStart(combined[left]) {
			left++
		}
		right := len(combined) - 25000
		for right < len(combined) && !utf8.RuneStart(combined[right]) {
			right++
		}
		combined = combined[:left] + "\n... [ output too large, truncated middle ] ...\n" + combined[right:]
	}
	return combined
}
