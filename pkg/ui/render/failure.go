package render

import (
	"fmt"
	"io"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
	"strings"
)

func FailureAdvice(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "401"), strings.Contains(lower, "403"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "credentials"):
		return "Check provider credentials with /doctor or sign in again."
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"), strings.Contains(lower, "dial tcp"):
		return "Check that the server is running, then use /doctor to verify the endpoint."
	case strings.Contains(lower, "certificate"), strings.Contains(lower, "tls"):
		return "Check the provider address and certificate settings with /doctor."
	case strings.Contains(lower, "429"), strings.Contains(lower, "rate limit"):
		return "Wait briefly, then retry the request."
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		return "Check server availability; increase the timeout if the server needs more time."
	case strings.Contains(lower, "read") && (strings.Contains(lower, "before") || strings.Contains(lower, "changed")):
		return "Read the current file, then retry the edit against its latest contents."
	case strings.Contains(lower, "oldtext"), strings.Contains(lower, "not found in file"):
		return "Read the affected lines and retry with an exact, unique match."
	case strings.Contains(lower, "permission denied"):
		return "Check the file permissions and workspace access."
	case strings.Contains(lower, "command not found"), strings.Contains(lower, "executable file not found"), strings.Contains(lower, "exit status 127"):
		return "Check the command name and installed programs with /doctor."
	case strings.Contains(lower, "request budget exhausted"):
		return "Check provider availability with /doctor, then retry."
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such file"):
		return "Check the path or identifier and try again."
	case strings.Contains(lower, "cancel"):
		return "The operation was stopped. You can continue when ready."
	default:
		return "Review the details below, correct the issue, then retry."
	}
}

func renderActionableFailure(w io.Writer, message string, collapse bool, theme style.UITheme) {
	message = strings.TrimSpace(SanitizeTerminalText(message))
	if message == "" {
		message = "The operation failed without a diagnostic."
	}
	width, _ := terminal.GetDimensions()
	width = max(1, width-4)
	first, _, _ := strings.Cut(message, "\n")
	first = style.TruncateCells(first, width)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render("✗ "+first))
	advice := FailureAdvice(message)
	for _, row := range ChatHistoryRows("Next: "+advice, width) {
		fmt.Fprintln(w, "  "+style.NewStyle().Foreground(theme.TextMuted).Render(row))
	}
	if collapse {
		fmt.Fprintln(w, "  Ctrl+O to inspect error details")
		return
	}
	if message != first {
		for _, row := range ChatHistoryRows(message, width) {
			fmt.Fprintln(w, "  "+row)
		}
	}
}
