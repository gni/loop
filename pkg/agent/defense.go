package agent

import (
	"regexp"
	"strings"
)

// FormatDefensiveError turns syntax errors into descriptive, action-oriented correction prompts.
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)

	if strings.Contains(errStr, "Recommendation:") {
		return RuntimeMessagef("alert_plain", "System Alert: Your execution of '%s' failed due to: %s", toolName, errStr)
	}
	if strings.Contains(lowerErr, "loop detected") || strings.Contains(lowerErr, "agent halted") {
		return RuntimeMessagef("alert_blocked", "System Alert: Your execution of '%s' was blocked: %s", toolName, errStr)
	}

	var suggestion string
	if strings.Contains(lowerErr, "not unique") {
		suggestion = RuntimeMessage("suggestion_not_unique", "The oldText block matches multiple locations in the file.")
	} else if (strings.Contains(lowerErr, "oldtext block") && strings.Contains(lowerErr, "not found")) ||
		strings.Contains(lowerErr, "targetcontent not found") {
		suggestion = RuntimeMessage("suggestion_old_text_miss", "The file exists, but oldText does not match its current contents.")
	} else if (strings.Contains(lowerErr, "task") && (strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no task"))) ||
		toolName == "task_status" || toolName == "task_kill" {
		suggestion = RuntimeMessage("suggestion_task_not_found", "The background task ID was not found.")
	} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no such file") {
		suggestion = RuntimeMessage("suggestion_path_not_found", "Inspect your immediate working directory structure using 'find' or 'list'.")
	} else if strings.Contains(lowerErr, "escapes workspace") || strings.Contains(lowerErr, "security violation") {
		suggestion = RuntimeMessage("suggestion_workspace", "Verify that the path is relative or inside the current workspace.")
	} else if strings.Contains(lowerErr, "command failed") || strings.Contains(lowerErr, "exit status") {
		suggestion = RuntimeMessage("suggestion_command", "Review the command syntax and arguments.")
	} else if strings.HasPrefix(lowerErr, "unknown tool:") {
		suggestion = RuntimeMessage("suggestion_unknown_tool", "Inspect <tools> in system instructions for valid tool names.")
	} else {
		suggestion = RuntimeMessage("suggestion_default", "Ensure arguments match the schema parameters exactly.")
	}

	return RuntimeMessagef("alert_with_recommendation", "System Alert: Your execution of '%s' failed due to: %s\nRecommendation: %s", toolName, errStr, suggestion)
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
				errMsg := strings.TrimPrefix(err.Error(), "command failed: ")
				return RuntimeMessagef("command_failed_header", "[Command Failed: %s]", errMsg) + "\n" + diagnostic
			}
			return diagnostic
		}
		return RuntimeMessagef("command_failed_header", "[Command Failed: %s]", err.Error())
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
