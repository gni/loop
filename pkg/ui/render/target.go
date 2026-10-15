package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func ExtractToolTarget(toolName string, argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}
	// Direct unquoted JSON string check (e.g. "python3 script.py")
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
			return strings.TrimSpace(unquoted)
		}
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
		repaired := RepairArgsJSON(trimmed)
		_ = json.Unmarshal([]byte(repaired), &argsMap)
	}

	getString := func(key string) string {
		if argsMap != nil {
			if val, ok := argsMap[key]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
			for _, wrapper := range []string{"parameters", "params", "input", "arguments"} {
				if subVal, ok := argsMap[wrapper]; ok {
					if subMap, ok := subVal.(map[string]interface{}); ok {
						if val, ok := subMap[key]; ok {
							if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
								return strings.TrimSpace(s)
							}
						}
					}
				}
			}
		}
		// Fallback regex with escaped quote support
		regex := regexp.MustCompile(fmt.Sprintf(`"(?i)%s"\s*:\s*"((?:\\.|[^"\\])*)"`, regexp.QuoteMeta(key)))
		matches := regex.FindStringSubmatch(trimmed)
		if len(matches) > 1 {
			var unquoted string
			if err := json.Unmarshal([]byte(`"`+matches[1]+`"`), &unquoted); err == nil && strings.TrimSpace(unquoted) != "" {
				return strings.TrimSpace(unquoted)
			}
			return strings.TrimSpace(matches[1])
		}
		return ""
	}

	// 1. Command execution tools (bash, run_command, exec, etc.)
	if IsCommandLikeTool(toolName) {
		for _, k := range []string{"CommandLine", "command", "cmd", "script", "code", "input", "arguments", "args", "c", "exec", "run", "shell", "sh"} {
			if c := getString(k); c != "" {
				return c
			}
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

	// 2. Search / query tools (grep, grep_search, search_web, find_files, etc.)
	if strings.Contains(toolName, "grep") || strings.Contains(toolName, "search") || strings.Contains(toolName, "find") || strings.Contains(toolName, "query") {
		q := getString("pattern")
		if q == "" {
			q = getString("Query")
		}
		if q == "" {
			q = getString("query")
		}

		p := getString("SearchPath")
		if p == "" {
			p = getString("path")
		}
		if p == "" {
			p = getString("DirectoryPath")
		}
		if p == "" {
			p = getString("dirPath")
		}

		if q != "" && p != "" {
			wd, err := os.Getwd()
			if err == nil {
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

	// 3. File / Directory tools
	if strings.Contains(toolName, "file") || strings.Contains(toolName, "dir") || strings.Contains(toolName, "read") || strings.Contains(toolName, "write") || strings.Contains(toolName, "edit") || strings.Contains(toolName, "replace") || toolName == "ls" || toolName == "view" {
		if p := getString("AbsolutePath"); p != "" {
			return p
		}
		if p := getString("TargetFile"); p != "" {
			return p
		}
		if p := getString("SearchPath"); p != "" {
			return p
		}
		if p := getString("DirectoryPath"); p != "" {
			return p
		}
		if p := getString("dirPath"); p != "" {
			return p
		}
		if p := getString("directory_path"); p != "" {
			return p
		}
		if p := getString("file_path"); p != "" {
			return p
		}
		if p := getString("filePath"); p != "" {
			return p
		}
		if p := getString("file"); p != "" {
			return p
		}
		if p := getString("path"); p != "" {
			return p
		}
		if p := getString("target"); p != "" {
			return p
		}
		if p := getString("Target"); p != "" {
			return p
		}
		if p := getString("target_file"); p != "" {
			return p
		}
		if p := getString("targetFile"); p != "" {
			return p
		}
		if p := getString("filename"); p != "" {
			return p
		}
		if p := getString("fileName"); p != "" {
			return p
		}
		if p := getString("file_name"); p != "" {
			return p
		}
		if p := getString("filepath"); p != "" {
			return p
		}
		if p := getString("destination"); p != "" {
			return p
		}
		if p := getString("dest"); p != "" {
			return p
		}
		if p := getString("output_file"); p != "" {
			return p
		}
		if p := getString("outfile"); p != "" {
			return p
		}
	}

	// 4. Subagents / Spawning / Prompts / Interactive
	if strings.Contains(toolName, "subagent") || strings.Contains(toolName, "spawn") || strings.Contains(toolName, "task") || strings.Contains(toolName, "ask") || strings.Contains(toolName, "permission") {
		if p := getString("question"); p != "" {
			return p
		}
		if p := getString("Question"); p != "" {
			return p
		}
		if p := getString("prompt"); p != "" {
			return p
		}
		if p := getString("Prompt"); p != "" {
			return p
		}
		if p := getString("name"); p != "" {
			return p
		}
		for _, k := range []string{"id", "task_id", "taskId", "task", "command", "cmd", "CommandLine"} {
			if p := getString(k); p != "" {
				return p
			}
		}
	}

	if strings.Contains(toolName, "todo") || strings.Contains(toolName, "plan") {
		if argsMap != nil {
			for _, key := range []string{"tasks", "todos", "items", "plan", "content", "data", "list", "todo_list"} {
				if tasksVal, ok := argsMap[key]; ok {
					if taskList, ok := tasksVal.([]interface{}); ok {
						var inProgressTask string
						var firstPendingTask string
						for _, item := range taskList {
							if m, ok := item.(map[string]interface{}); ok {
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
							} else if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
								if firstPendingTask == "" {
									firstPendingTask = strings.TrimSpace(s)
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
		}
		return ""
	}

	// 5. Fallback - check all keys in priority order
	keys := []string{
		"CommandLine", "command", "query", "Query", "pattern", "prompt", "Prompt",
		"AbsolutePath", "TargetFile", "SearchPath", "DirectoryPath", "dirPath",
		"file_path", "filePath", "file", "path", "target", "Target", "target_file", "targetFile", "filename", "fileName", "name", "id",
	}
	for _, key := range keys {
		if val := getString(key); val != "" {
			return val
		}
	}

	return ""
}

// ExtractToolInstructions retrieves user- or model-supplied instructions/descriptions from tool argument payloads.
func ExtractToolInstructions(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return ""
	}

	var argsMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &argsMap); err != nil {
		repaired := RepairArgsJSON(trimmed)
		_ = json.Unmarshal([]byte(repaired), &argsMap)
	}

	instructionKeys := []string{
		"instructions", "instruction",
		"Instructions", "Instruction",
		"description", "Description",
		"comment", "Comment",
		"explanation", "Explanation",
		"summary", "Summary",
		"message", "Message",
	}

	if argsMap != nil {
		for _, k := range instructionKeys {
			if val, ok := argsMap[k]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}

		for _, wrapper := range []string{"parameters", "params", "input", "arguments", "edit"} {
			if subVal, ok := argsMap[wrapper]; ok {
				if subMap, ok := subVal.(map[string]interface{}); ok {
					for _, k := range instructionKeys {
						if val, ok := subMap[k]; ok {
							if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
								return strings.TrimSpace(s)
							}
						}
					}
				}
			}
		}

		for _, listKey := range []string{"updates", "edits", "replacements"} {
			if listVal, ok := argsMap[listKey]; ok {
				if slice, ok := listVal.([]interface{}); ok {
					var itemInstructions []string
					for _, item := range slice {
						if itemMap, ok := item.(map[string]interface{}); ok {
							for _, k := range instructionKeys {
								if val, ok := itemMap[k]; ok {
									if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
										itemInstructions = append(itemInstructions, strings.TrimSpace(s))
										break
									}
								}
							}
						}
					}
					if len(itemInstructions) > 0 {
						return strings.Join(itemInstructions, "; ")
					}
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
