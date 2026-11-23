package render

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/render/scrollback"
	"loop/pkg/ui/style"
)

var failureHTTPStatus = regexp.MustCompile(`\b(?:http(?: status(?: code)?)?|status(?: code)?)[\s:=]+(401|403|429)\b`)

// Tool output can contain arbitrary source, paths, and subprocess diagnostics.
// Provider-specific advice belongs only to failures outside a tool result.
func failureAdvice(message, toolName string) string {
	lower := strings.ToLower(message)
	providerFailure := toolName == ""
	fileOperation := toolName == "edit" || toolName == "write" || toolName == "read"
	status := ""
	if match := failureHTTPStatus.FindStringSubmatch(lower); match != nil {
		status = match[1]
	}
	switch {
	case strings.Contains(lower, "context canceled"), strings.Contains(lower, "context cancelled"), strings.Contains(lower, "aborted by user"):
		return "The operation was stopped. You can continue when ready."
	case strings.Contains(lower, "task token budget exhausted"):
		return "The task reached its token budget. Review the remaining work and budget before continuing."
	case strings.Contains(lower, "inference request budget exhausted"):
		return "The inference retry limit was reached. Review the preceding request failures before retrying."
	case providerFailure && (status == "401" || status == "403" || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "credentials")):
		return "Check provider credentials with /doctor or sign in again."
	case providerFailure && (strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") || strings.Contains(lower, "dial tcp")):
		return "Check that the server is running, then use /doctor to verify the endpoint."
	case providerFailure && (strings.Contains(lower, "certificate") || strings.Contains(lower, "tls")):
		return "Check the provider address and certificate settings with /doctor."
	case providerFailure && (status == "429" || strings.Contains(lower, "rate limit")):
		return "Wait briefly, then retry the request."
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		if !providerFailure {
			return "The tool timed out and may already have made changes. Inspect its output and current state before retrying."
		}
		return "Check server availability; increase the timeout if the server needs more time."
	case fileOperation && (strings.Contains(lower, "recovery [invalid_arguments]") || strings.Contains(lower, "invalid edit arguments") || strings.Contains(lower, "invalid write arguments") || strings.Contains(lower, "invalid json tool arguments") || strings.Contains(lower, "missing required argument:") || strings.Contains(lower, "oldtext must be") || strings.Contains(lower, "newtext must be") || strings.HasPrefix(lower, "tool \""+toolName+"\" arguments:")):
		switch toolName {
		case "edit":
			return "Correct the edit arguments: provide a path, nonempty oldText, and explicit newText; an empty newText deletes text."
		case "write":
			return "Correct the write arguments: provide a path and explicit content; an empty content string clears the file."
		default:
			return "Correct the read arguments: provide a path and use nonnegative integers for offset and limit."
		}
	case fileOperation && (strings.Contains(lower, "recovery [syntax_invalid]") || strings.Contains(lower, "syntax rejected before saving")):
		return "The candidate was not saved. Correct the replacement syntax before retrying."
	case fileOperation && (strings.Contains(lower, "recovery [read_required]") || strings.Contains(lower, "recovery [revision_changed]") || strings.Contains(lower, "file has not been read") || strings.Contains(lower, "changed since your last read") || strings.Contains(lower, "modified on disk since your last read")):
		return "Read the current file before retrying the requested change."
	case toolName == "edit" && (strings.Contains(lower, "recovery [ambiguous_edit]") || strings.Contains(lower, "oldtext is not unique")):
		return "Include surrounding lines in oldText so it identifies exactly one occurrence."
	case toolName == "edit" && (strings.Contains(lower, "recovery [edit_not_found]") || strings.Contains(lower, "oldtext was not found") || strings.Contains(lower, "not found in file")):
		return "Read the affected lines and retry with an exact, unique match."
	case strings.Contains(lower, "permission denied"):
		return "Check the file permissions and workspace access."
	case toolName == "bash" && (strings.Contains(lower, "command not found") || strings.Contains(lower, "executable file not found") || strings.Contains(lower, "exit status 127") || strings.Contains(lower, "[command exit code: 127]")):
		return "Check the command name, PATH, and the project's installed dependencies, then retry."
	case (toolName == "get_task_status" || toolName == "stop_task") && (strings.Contains(lower, "not found") || strings.Contains(lower, "no task")):
		return "Check the background task list or events for a valid task ID."
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such file"):
		return "Check the path or identifier and try again."
	default:
		return "Review the reported error and correct its cause before retrying."
	}
}

func renderActionableFailure(w io.Writer, message string, collapse bool, theme style.UITheme) {
	renderFailure(w, message, "", collapse, theme)
}

func renderFailure(w io.Writer, message, toolName string, collapse bool, theme style.UITheme) {
	message = strings.TrimSpace(SanitizeTerminalText(message))
	if message == "" {
		message = "The operation failed without a diagnostic."
	}
	width := max(1, markdown.RenderWidth(w)-4)
	first, _, _ := strings.Cut(message, "\n")
	first = style.TruncateCells(first, width)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render("✗ "+first))
	advice := failureAdvice(message, toolName)
	for _, row := range scrollback.ChatHistoryRows("Next: "+advice, width) {
		fmt.Fprintln(w, "  "+style.NewStyle().Foreground(theme.TextMuted).Render(row))
	}
	if collapse {
		fmt.Fprintln(w, "  Ctrl+O to inspect error details")
		return
	}
	if message != first {
		for _, row := range scrollback.ChatHistoryRows(message, width) {
			fmt.Fprintln(w, "  "+row)
		}
	}
}
