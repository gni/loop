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
	reJSONStringKV = regexp.MustCompile(`"([a-zA-Z0-9_]+)"\s*:\s*"((?:\\.|[^"\\])*)"`)

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
	todoListKeys = []string{
		"tasks", "todos", "items", "plan", "content", "data", "list", "todo_list",
	}
	todoDescKeys = []string{
		"task", "content", "description", "title", "name", "text", "desc", "summary",
	}
)

func parseToolArgsMap(trimmed string) map[string]interface{} {
	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
		_ = json.Unmarshal([]byte(RepairArgsJSON(trimmed)), &argsMap)
	}
	return argsMap
}

func extractRawJSONStrings(raw string) map[string]string {
	matches := reJSONStringKV.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		k := strings.ToLower(m[1])
		if _, exists := out[k]; exists {
			continue
		}
		var unquoted string
		if err := json.Unmarshal([]byte(`"`+m[2]+`"`), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
			out[k] = strings.TrimSpace(unquoted)
		} else if trimmed := strings.TrimSpace(m[2]); trimmed != "" {
			out[k] = trimmed
		}
	}
	return out
}

func findMapStringByKeys(m map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func extractTodoTargetFromMap(argsMap map[string]interface{}) string {
	if argsMap == nil {
		return ""
	}
	for _, key := range todoListKeys {
		taskList, ok := argsMap[key].([]interface{})
		if !ok {
			continue
		}
		var inProgressTask, firstPendingTask string
		for _, item := range taskList {
			switch m := item.(type) {
			case map[string]interface{}:
				taskDesc := findMapStringByKeys(m, todoDescKeys)
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
			if inProgressTask != "" {
				break
			}
		}
		if inProgressTask != "" {
			return inProgressTask
		}
		if firstPendingTask != "" {
			return firstPendingTask
		}
	}
	return ""
}

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

	argsMap := parseToolArgsMap(trimmed)
	var regexPairs map[string]string

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
			if regexPairs == nil {
				regexPairs = extractRawJSONStrings(trimmed)
			}
			if v := regexPairs[strings.ToLower(key)]; v != "" {
				return v
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
		return extractTodoTargetFromMap(argsMap)
	}

	return getString(fallbackTargetKeys...)
}

func ExtractToolInstructions(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}

	if argsMap := parseToolArgsMap(trimmed); argsMap != nil {
		if s := findMapStringByKeys(argsMap, instructionKeys); s != "" {
			return s
		}
		for _, wrapper := range []string{"parameters", "params", "input", "arguments", "edit"} {
			if subMap, ok := argsMap[wrapper].(map[string]interface{}); ok {
				if s := findMapStringByKeys(subMap, instructionKeys); s != "" {
					return s
				}
			}
		}
		for _, listKey := range []string{"updates", "edits", "replacements"} {
			if slice, ok := argsMap[listKey].([]interface{}); ok {
				var itemInstructions []string
				for _, item := range slice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if s := findMapStringByKeys(itemMap, instructionKeys); s != "" {
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

	regexPairs := extractRawJSONStrings(trimmed)
	for _, k := range instructionKeys {
		if v := regexPairs[strings.ToLower(k)]; v != "" {
			return v
		}
	}

	return ""
}