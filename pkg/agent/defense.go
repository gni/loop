package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// FormatDefensiveError turns syntax errors into descriptive, action-oriented correction prompts.
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)

	if strings.Contains(errStr, "Recommendation:") {
		return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s", toolName, errStr)
	}
	if strings.Contains(lowerErr, "loop detected") || strings.Contains(lowerErr, "agent halted") {
		return fmt.Sprintf("System Alert: Your execution of '%s' was blocked: %s", toolName, errStr)
	}

	var suggestion string
	if strings.Contains(lowerErr, "not unique") {
		suggestion = "The oldText block matches multiple locations in the file. Include more surrounding lines in oldText to make it unique, or read the file again to copy the exact unique block."
	} else if (strings.Contains(lowerErr, "oldtext block") && strings.Contains(lowerErr, "not found")) ||
		strings.Contains(lowerErr, "targetcontent not found") {
		suggestion = "The file exists, but oldText does not match its current contents. Read the file again, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write."
	} else if (strings.Contains(lowerErr, "task") && (strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no task"))) ||
		toolName == "task_status" || toolName == "task_kill" {
		suggestion = "The background task ID was not found. Check background tasks list or events to inspect valid task IDs. Do not search the filesystem for task IDs."
	} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no such file") {
		suggestion = "Inspect your immediate working directory structure using 'find' or 'list' or check the file path. Ensure the file actually exists before calling this tool."
	} else if strings.Contains(lowerErr, "escapes workspace") || strings.Contains(lowerErr, "security violation") {
		suggestion = "Verify that the path is relative or inside the current workspace. Escaping the workspace is blocked."
	} else if strings.Contains(lowerErr, "command failed") || strings.Contains(lowerErr, "exit status") {
		suggestion = "Review the command syntax and arguments. If the command depends on specific environment setups or files, verify they are present."
	} else if strings.HasPrefix(lowerErr, "unknown tool:") {
		suggestion = "Inspect <tools> in system instructions for valid tool names. For example, use 'write' instead of 'write_path' or 'write_file', and 'edit' instead of 'edit_file'."
	} else {
		suggestion = "Ensure arguments match the schema parameters exactly, and that any files/folders referred to exist and are spelled correctly."
	}

	return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s\nRecommendation: %s", toolName, errStr, suggestion)
}

// FormatToolExecutionFailure preserves useful tool diagnostics while adding the
// action-oriented failure context expected by the agent and terminal UI.
func FormatToolExecutionFailure(toolName, output string, err error) string {
	if err == nil {
		return strings.TrimRight(output, "\r\n")
	}
	diagnostic := strings.Trim(output, "\r\n")
	if toolName == "bash" {
		if diagnostic != "" {
			if strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
				return diagnostic
			}
			if !strings.HasPrefix(strings.ToLower(diagnostic), "[command failed") && !strings.HasPrefix(strings.ToLower(diagnostic), "command failed") && !strings.HasPrefix(diagnostic, "Error:") {
				errMsg := err.Error()
				if strings.HasPrefix(errMsg, "command failed: ") {
					errMsg = strings.TrimPrefix(errMsg, "command failed: ")
				}
				return fmt.Sprintf("[command failed: %s]\n%s", errMsg, diagnostic)
			}
			return diagnostic
		}
		return fmt.Sprintf("[command failed: %s]", err.Error())
	}
	alert := FormatDefensiveError(toolName, err)
	if strings.TrimSpace(diagnostic) == "" || strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
		return alert
	}
	return diagnostic + "\n\n" + alert
}

var llmControlTokenRegexes = []*regexp.Regexp{
	regexp.MustCompile(`</?atem:[^>\n<]*>?`),
	regexp.MustCompile(`</?atem:[^<\n]*`),
	regexp.MustCompile(`<\|(?:eom|im_start|im_end|start|message|endoftext|assistant|user|system)[^>|]*\|?>`),
	regexp.MustCompile(`<\|[^>\n|]+\|>`),
}

// SanitizeLLMControlTokens removes leaked provider control tokens and ChatML tags from text.
func SanitizeLLMControlTokens(s string) string {
	for _, re := range llmControlTokenRegexes {
		s = re.ReplaceAllString(s, "")
	}
	return s
}
