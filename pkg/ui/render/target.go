package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commandKeys = []string{
		"CommandLine", "command", "cmd", "script", "code", "input",
		"arguments", "args", "c", "exec", "run", "shell", "sh",
	}
	fileTargetKeys = []string{
		"AbsolutePath", "TargetFile", "SearchPath", "DirectoryPath",
		"dirPath", "directory_path", "file_path", "filePath", "file",
		"path", "target", "Target", "target_file", "targetFile",
		"filename", "fileName", "file_name", "filepath", "destination",
		"dest", "output_file", "outfile",
	}
	subagentKeys = []string{
		"question", "Question", "prompt", "Prompt", "name",
		"id", "task_id", "taskId", "task", "command", "cmd", "CommandLine",
	}
	fallbackTargetKeys = []string{
		"CommandLine", "command", "query", "Query", "pattern", "prompt", "Prompt",
		"AbsolutePath", "TargetFile", "SearchPath", "DirectoryPath", "dirPath",
		"file_path", "filePath", "file", "path", "target", "Target",
		"target_file", "targetFile", "filename", "fileName", "name", "id",
	}
	instructionKeys = []string{
		"instructions", "instruction", "Instructions", "Instruction",
		"description", "Description", "comment", "Comment",
		"explanation", "Explanation", "summary", "Summary",
		"message", "Message",
	}
)

func ExtractToolTarget(toolName string, argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
			return strings.TrimSpace(unquoted)
		}
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
		_ = json.Unmarshal([]byte(RepairArgsJSON(trimmed)), &argsMap)
	}

	getString := func(keys ...string) string {
		for _, key := range keys {
			if argsMap != nil {
				if s, ok := argsMap[key].(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
				for _, wrapper := range []string{"parameters", "params", "input", "arguments"} {
					if subMap, ok := argsMap[wrapper].(map[string]interface{}); ok {
						if s, ok := subMap[key].(string); ok && strings.TrimSpace(s) != "" {
							return strings.TrimSpace(s)
						}
					}
				}
			}
			regex := regexp.MustCompile(fmt.Sprintf(`"(?i)%s"\s*:\s*"((?:\\.|[^"\\])*)"`, regexp.QuoteMeta(key)))
			if matches := regex.FindStringSubmatch(trimmed); len(matches) > 1 {
				var unquoted string
				if err := json.Unmarshal([]byte(`"`+matches[1]+`"`), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
					return strings.TrimSpace(unquoted)
				}
				return strings.TrimSpace(matches[1])
			}
		}
		return ""
	}

	if IsCommandLikeTool(toolName) {
		if c := getString(commandKeys...); c != "" {
			return c
		}
		if len(argsMap) == 1 {
			for _, v := range argsMap {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			return trimmed
		}
	}

	if strings.Contains(toolName, "grep") || strings.Contains(toolName, "search") || strings.Contains(toolName, "find") || strings.Contains(toolName, "query") {
		q := getString("pattern", "Query", "query")
		p := getString("SearchPath", "path", "DirectoryPath", "dirPath")
		if q != "" && p != "" {
			if wd, err := os.Getwd(); err == nil {
				if rel, err := filepath.Rel(wd, p); err == nil {
					p = rel
				}
			}
			return fmt.Sprintf("%s (in %s)", q, p)
		}
		if q != "" {
			return q
		}
		if p != "" {
			return p
		}
	}

	if strings.Contains(toolName, "file") || strings.Contains(toolName, "dir") || strings.Contains(toolName, "read") || strings.Contains(toolName, "write") || strings.Contains(toolName, "edit") || strings.Contains(toolName, "replace") || toolName == "ls" || toolName == "view" {
		if p := getString(fileTargetKeys...); p != "" {
			return p
		}
	}

	if strings.Contains(toolName, "subagent") || strings.Contains(toolName, "spawn") || strings.Contains(toolName, "task") || strings.Contains(toolName, "ask") || strings.Contains(toolName, "permission") {
		if p := getString(subagentKeys...); p != "" {
			return p
		}
	}

	if strings.Contains(toolName, "todo") || strings.Contains(toolName, "plan") {
		if argsMap != nil {
			for _, key := range []string{"tasks", "todos", "items", "plan", "content", "data", "list", "todo_list"} {
				if taskList, ok := argsMap[key].([]interface{}); ok {
					var inProgressTask, firstPendingTask string
					for _, item := range taskList {
						switch m := item.(type) {
						case map[string]interface{}:
							var taskDesc string
							for _, descKey := range []string{"task", "content", "description", "title", "name", "text", "desc", "summary"} {
								if s, ok := m[descKey].(string); ok && strings.TrimSpace(s) != "" {
									taskDesc = strings.TrimSpace(s)
									break
								}
							}
							status, _ := m["status"].(string)
							if strings.EqualFold(status, "in_progress") && taskDesc != "" {
								inProgressTask = taskDesc
								break
							}
							if firstPendingTask == "" && taskDesc != "" {
								firstPendingTask = taskDesc
							}
						case string:
							if firstPendingTask == "" && strings.TrimSpace(m) != "" {
								firstPendingTask = strings.TrimSpace(m)
							}
						}
					}
					if inProgressTask != "" {
						return inProgressTask
					}
					if firstPendingTask != "" {
						return firstPendingTask
					}
				}
			}
		}
		return ""
	}

	return getString(fallbackTargetKeys...)
}

func ExtractToolInstructions(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
		_ = json.Unmarshal([]byte(RepairArgsJSON(trimmed)), &argsMap)
	}

	findInstructionInMap := func(m map[string]interface{}) string {
		for _, k := range instructionKeys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}

	if argsMap != nil {
		if s := findInstructionInMap(argsMap); s != "" {
			return s
		}
		for _, wrapper := range []string{"parameters", "params", "input", "arguments", "edit"} {
			if subMap, ok := argsMap[wrapper].(map[string]interface{}); ok {
				if s := findInstructionInMap(subMap); s != "" {
					return s
				}
			}
		}
		for _, listKey := range []string{"updates", "edits", "replacements"} {
			if slice, ok := argsMap[listKey].([]interface{}); ok {
				var itemInstructions []string
				for _, item := range slice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if s := findInstructionInMap(itemMap); s != "" {
							itemInstructions = append(itemInstructions, s)
						}
					}
				}
				if len(itemInstructions) > 0 {
					return strings.Join(itemInstructions, "; ")
				}
			}
		}
	}

	for _, k := range instructionKeys {
		regex := regexp.MustCompile(fmt.Sprintf(`"(?i)%s"\s*:\s*"((?:\\.|[^"\\])*)"`, regexp.QuoteMeta(k)))
		if matches := regex.FindStringSubmatch(trimmed); len(matches) > 1 {
			var unquoted string
			if err := json.Unmarshal([]byte(`"`+matches[1]+`"`), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
				return strings.TrimSpace(unquoted)
			}
			return strings.TrimSpace(matches[1])
		}
	}

	return ""
}