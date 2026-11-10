package agent

import (
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/domain/diagnostic"
)

// FormatDefensiveError turns syntax errors into concise, actionable error messages.
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)
	// Todo diagnostics already describe their schema or plan-state error.
	if toolName == "todo" {
		return errStr
	}
	if strings.HasPrefix(toolName, "mcp__") || toolName == "read_mcp_resource" ||
		toolName == "list_mcp_resources" || toolName == "list_mcp_resource_templates" {
		return fmt.Sprintf("error [%s]: %s", toolName, errStr)
	}

	if strings.Contains(lowerErr, "loop detected") || strings.Contains(lowerErr, "agent halted") {
		return fmt.Sprintf("error: %s (Action required: Backtrack and explore an alternative approach or different strategy instead of repeating this action.)", errStr)
	}

	var detail string
	if strings.Contains(lowerErr, "not unique") {
		detail = RuntimeMessage("suggestion_not_unique", "The oldText block matches multiple locations in the file. Include more surrounding lines in oldText to make it unique, or read the file again to copy the exact unique block.")
	} else if (strings.Contains(lowerErr, "oldtext") && strings.Contains(lowerErr, "not found")) ||
		strings.Contains(lowerErr, "targetcontent not found") {
		detail = RuntimeMessage("suggestion_old_text_miss", "The file exists, but oldText does not match its current contents. Read the file again, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write.")
	} else if (toolName == "get_task_status" || toolName == "stop_task") &&
		(strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no task")) {
		detail = RuntimeMessage("suggestion_task_not_found", "The background task ID was not found. Check background tasks list or events to inspect valid task IDs. Do not search the filesystem for task IDs.")
	} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no such file") {
		detail = RuntimeMessage("suggestion_path_not_found", "Inspect your immediate working directory structure using 'find_files' or 'list_directory' or check the file path. Ensure the file actually exists before calling this tool.")
	} else if strings.Contains(lowerErr, "escapes workspace") || strings.Contains(lowerErr, "outside workspace root") {
		detail = RuntimeMessage("suggestion_workspace", "Verify that the path is relative or inside the current workspace. Escaping the workspace is blocked.")
	} else if strings.Contains(lowerErr, "command failed") || strings.Contains(lowerErr, "exit status") {
		detail = RuntimeMessage("suggestion_command", "Review the command syntax and arguments. If attempting to read, write, edit, or search files, use internal tools ('read', 'write', 'edit', 'grep', 'find_files', 'list_directory') instead of shell commands.")
	} else if strings.HasPrefix(lowerErr, "unknown tool:") {
		detail = RuntimeMessage("suggestion_unknown_tool", "Inspect <tools> in system instructions for valid tool names. For example, use 'write' instead of 'write_path' or 'write_file', and 'edit' instead of 'edit_file'.")
	}

	if detail != "" && !strings.Contains(lowerErr, detail) && !strings.Contains(lowerErr, "closest match near line") {
		return fmt.Sprintf("error [%s]: %s (%s)", toolName, errStr, detail)
	}
	return fmt.Sprintf("error [%s]: %s", toolName, errStr)
}

// FormatToolExecutionFailure preserves useful tool diagnostics while adding the
// action-oriented failure context expected by the agent and terminal UI.
func FormatToolExecutionFailure(toolName, output string, err error, hints ...*diagnostic.Recovery) string {
	if err == nil {
		return strings.TrimRight(output, "\r\n")
	}
	diagnostic := strings.Trim(output, "\r\n")
	recovery := tool.RecoveryFor(toolName, "", err)
	if len(hints) > 0 {
		recovery = hints[0]
	}
	if recovery != nil {
		if diagnostic == "" {
			diagnostic = err.Error()
		} else if diagnostic != err.Error() {
			diagnostic += "\n" + err.Error()
		}
		diagnostic += fmt.Sprintf("\n\nRecovery [%s]: %s", recovery.Code, recovery.Instruction)
		if recovery.Tool != "" {
			diagnostic += fmt.Sprintf("\nSuggested next call: %s %s", recovery.Tool, recovery.Arguments)
		}
		return diagnostic
	}
	if toolName == "bash" {
		if diagnostic != "" {
			if strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
				return diagnostic
			}
			if strings.Contains(diagnostic, "exit status") || strings.Contains(diagnostic, "exit code") || strings.Contains(diagnostic, "command timed out") {
				return diagnostic
			}
			if !strings.HasPrefix(strings.ToLower(diagnostic), "[command failed") && !strings.HasPrefix(strings.ToLower(diagnostic), "command failed") && !strings.HasPrefix(diagnostic, "Error:") {
				errMsg := strings.TrimPrefix(err.Error(), "command failed: ")
				return fmt.Sprintf("command failed: %s\n%s", errMsg, diagnostic)
			}
			return diagnostic
		}
		return fmt.Sprintf("command failed: %s", err.Error())
	}
	alert := FormatDefensiveError(toolName, err)
	if strings.TrimSpace(diagnostic) == "" || strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
		return alert
	}
	return diagnostic + "\n\n" + alert
}
