package fallback

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

var (
	reToolCallAttr = regexp.MustCompile(`<tool_call\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/tool_call>`)
	reToolColon    = regexp.MustCompile(`<tool:([a-zA-Z0-9_\-]+)>(?s:(.*?))<\/tool:([a-zA-Z0-9_\-]+)>`)
	reExecuteAttr  = regexp.MustCompile(`<execute\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/execute>`)
	reBlock        = regexp.MustCompile(`(?s)<tool_call\s*>?(.*?)(?:<\/tool_call\s*>|$)`)
	reFunc         = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/function(?:\s*>|\s*\n|$)|$)`)
	reParam        = regexp.MustCompile(`(?s)<parameter[=\s]*([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/parameter(?:\s*>|\s*\n|$)|$)`)
)

func appendNamedCalls(toolCalls *[]db.ToolCall, re *regexp.Regexp, content string) {
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) >= 3 {
			*toolCalls = append(*toolCalls, BuildToolCall(match[1], strings.TrimSpace(match[2])))
		}
	}
}

// ParseXMLAttrs extracts key="val", key='val', or key=val attribute pairs from XML tag string.
func ParseXMLAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	reAttr := regexp.MustCompile(`([a-zA-Z0-9_\-]+)=(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		key := m[1]
		val := m[2]
		if val == "" && m[3] != "" {
			val = m[3]
		}
		if val == "" && m[4] != "" {
			val = m[4]
		}
		attrs[key] = val
	}
	return attrs
}

// BuildXMLToolCall constructs a structured db.ToolCall from an XML tag and its body.
func BuildXMLToolCall(name string, attrs map[string]string, body string) db.ToolCall {
	name = tool.NormalizeName(name)
	body = strings.TrimSpace(body)

	// If body is already valid JSON, merge tag attributes into it if not present
	var temp map[string]interface{}
	if json.Unmarshal([]byte(body), &temp) == nil {
		for k, v := range attrs {
			if _, exists := temp[k]; !exists {
				temp[k] = v
			}
		}
		if merged, err := json.Marshal(temp); err == nil {
			return BuildToolCall(name, string(merged))
		}
		return BuildToolCall(name, body)
	}

	argsMap := make(map[string]interface{})
	for k, v := range attrs {
		argsMap[k] = v
	}

	switch name {
	case "write":
		path := attrs["path"]
		if path == "" {
			path = attrs["file"]
		}
		if path == "" {
			path = attrs["target"]
		}
		if path == "" {
			if reP := regexp.MustCompile(`(?s)<path>(.*?)<\/path>`).FindStringSubmatch(body); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		content := body
		if reC := regexp.MustCompile(`(?s)<content>(.*?)<\/content>`).FindStringSubmatch(body); len(reC) >= 2 {
			content = reC[1]
		}
		argsMap["path"] = path
		argsMap["content"] = content

	case "edit":
		path := attrs["path"]
		if path == "" {
			path = attrs["file"]
		}
		if path == "" {
			if reP := regexp.MustCompile(`(?s)<path>(.*?)<\/path>`).FindStringSubmatch(body); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		argsMap["path"] = path

		instructions := attrs["instructions"]
		if instructions == "" {
			instructions = attrs["instruction"]
		}
		if instructions == "" {
			instructions = attrs["description"]
		}
		if instructions == "" {
			if reI := regexp.MustCompile(`(?s)<(?:instructions?|description)>(.*?)<\/(?:instructions?|description)>`).FindStringSubmatch(body); len(reI) >= 2 {
				instructions = strings.TrimSpace(reI[1])
			}
		}
		if instructions != "" {
			argsMap["instructions"] = instructions
		}

		reOldNew := regexp.MustCompile(`(?s)<old>(.*?)<\/old>\s*<new>(.*?)<\/new>`)
		oldNewMatches := reOldNew.FindAllStringSubmatch(body, -1)
		if len(oldNewMatches) > 0 {
			var updates []map[string]string
			for _, m := range oldNewMatches {
				updates = append(updates, map[string]string{
					"oldText": m[1],
					"newText": m[2],
				})
			}
			argsMap["updates"] = updates
		} else {
			reConflict := regexp.MustCompile(`(?s)<<<<<<< SEARCH\r?\n(.*?)\r?\n=======\r?\n(.*?)\r?\n>>>>>>> REPLACE`)
			conflictMatches := reConflict.FindAllStringSubmatch(body, -1)
			if len(conflictMatches) > 0 {
				var updates []map[string]string
				for _, m := range conflictMatches {
					updates = append(updates, map[string]string{
						"oldText": m[1],
						"newText": m[2],
					})
				}
				argsMap["updates"] = updates
			} else if body != "" {
				argsMap["content"] = body
			}
		}

	case "bash":
		cmd := attrs["command"]
		if cmd == "" {
			cmd = body
		}
		argsMap["command"] = cmd
		if b, ok := attrs["background"]; ok && (b == "true" || b == "1") {
			argsMap["background"] = true
		}

	case "read":
		path := attrs["path"]
		if path == "" {
			path = body
		}
		argsMap["path"] = path

	case "grep", "find":
		pat := attrs["pattern"]
		if pat == "" {
			pat = body
		}
		argsMap["pattern"] = pat

	case "list":
		p := attrs["path"]
		if p == "" {
			p = body
		}
		if p == "" {
			p = "."
		}
		argsMap["path"] = p

	case "todo":
		reTask := regexp.MustCompile(`(?s)<(?:task|item)(?:\s+([^>]*))?>\s*(.*?)\s*<\/(?:task|item)>`)
		taskMatches := reTask.FindAllStringSubmatch(body, -1)
		if len(taskMatches) > 0 {
			var tasks []map[string]string
			for idx, tm := range taskMatches {
				tAttrs := ParseXMLAttrs(tm[1])
				id := tAttrs["id"]
				if id == "" {
					id = fmt.Sprintf("task_%d", idx+1)
				}
				status := tAttrs["status"]
				if status == "" {
					status = "pending"
				}
				taskDesc := strings.TrimSpace(tm[2])
				if taskDesc == "" {
					taskDesc = tAttrs["task"]
				}
				if taskDesc == "" {
					taskDesc = tAttrs["content"]
				}
				if taskDesc == "" {
					taskDesc = tAttrs["description"]
				}
				if taskDesc != "" {
					tasks = append(tasks, map[string]string{
						"id":     id,
						"task":   taskDesc,
						"status": status,
					})
				}
			}
			if len(tasks) > 0 {
				argsMap["tasks"] = tasks
			}
		} else {
			lines := strings.Split(body, "\n")
			var tasks []map[string]string
			taskIdx := 1
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				status := "pending"
				if strings.HasPrefix(line, "- [x]") || strings.HasPrefix(line, "[x]") || strings.HasPrefix(line, "- [X]") || strings.HasPrefix(line, "[X]") {
					status = "completed"
					line = strings.TrimSpace(strings.TrimLeft(line, "- [xX] "))
				} else if strings.HasPrefix(line, "- [>]") || strings.HasPrefix(line, "[>]") || strings.HasPrefix(line, "- [~]") || strings.HasPrefix(line, "[~]") || strings.HasPrefix(line, "- [in_progress]") {
					status = "in_progress"
					line = strings.TrimSpace(strings.TrimLeft(line, "- [>~in_progress] "))
				} else if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "[ ]") {
					status = "pending"
					line = strings.TrimSpace(strings.TrimLeft(line, "- [ ] "))
				} else if strings.HasPrefix(line, "• ") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
					line = strings.TrimSpace(line[2:])
				}
				if len(line) > 2 && (line[1] == '.' || line[1] == ')') && line[0] >= '0' && line[0] <= '9' {
					line = strings.TrimSpace(line[2:])
				}
				if line != "" {
					tasks = append(tasks, map[string]string{
						"id":     fmt.Sprintf("task_%d", taskIdx),
						"task":   line,
						"status": status,
					})
					taskIdx++
				}
			}
			if len(tasks) > 0 {
				argsMap["tasks"] = tasks
			}
		}

	default:
		if len(argsMap) == 0 && body != "" {
			return BuildToolCall(name, body)
		}
	}

	jsonBytes, err := json.Marshal(argsMap)
	if err != nil {
		return BuildToolCall(name, body)
	}
	return BuildToolCall(name, string(jsonBytes))
}

// ParseFallbackToolCalls extracts XML-style fallback tool calls from plain assistant message content.
func ParseFallbackToolCalls(content string) []db.ToolCall {
	var toolCalls []db.ToolCall

	// 1. Direct tool tags: <write path="...">content</write>, <edit path="...">content</edit>, <bash>cmd</bash>, etc.
	directToolNames := []string{"write", "edit", "bash", "read", "grep", "find", "list", "todo", "ask_user"}
	for _, toolName := range directToolNames {
		// Self-closing: <read path="foo" />
		reSelfClosing := regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b([^>]*?)\/>`, toolName))
		for _, match := range reSelfClosing.FindAllStringSubmatch(content, -1) {
			attrs := ParseXMLAttrs(match[1])
			toolCalls = append(toolCalls, BuildXMLToolCall(toolName, attrs, ""))
		}

		// Full pair: <write path="foo">content</write>
		rePair := regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b([^>]*?)>(.*?)<\/%s>`, toolName, toolName))
		for _, match := range rePair.FindAllStringSubmatch(content, -1) {
			attrs := ParseXMLAttrs(match[1])
			toolCalls = append(toolCalls, BuildXMLToolCall(toolName, attrs, match[2]))
		}
	}

	// 2. Match <tool_call ...>args</tool_call>
	reToolCall := regexp.MustCompile(`(?s)<tool_call\b([^>]*)>(.*?)<\/tool_call>`)
	for _, match := range reToolCall.FindAllStringSubmatch(content, -1) {
		attrs := ParseXMLAttrs(match[1])
		name := attrs["name"]
		if name != "" {
			toolCalls = append(toolCalls, BuildXMLToolCall(name, attrs, match[2]))
		} else {
			// Bare tool_call without name attr, check if body has function=...
			body := match[2]
			for _, funcMatch := range reFunc.FindAllStringSubmatch(body, -1) {
				if len(funcMatch) >= 3 {
					params := make(map[string]string)
					for _, p := range reParam.FindAllStringSubmatch(funcMatch[2], -1) {
						params[p[1]] = strings.TrimSpace(p[2])
					}
					if len(params) == 0 {
						toolCalls = append(toolCalls, BuildToolCall(funcMatch[1], strings.TrimSpace(funcMatch[2])))
					} else {
						args, err := json.Marshal(params)
						if err != nil {
							toolCalls = append(toolCalls, BuildToolCall(funcMatch[1], strings.TrimSpace(funcMatch[2])))
						} else {
							toolCalls = append(toolCalls, BuildToolCall(funcMatch[1], string(args)))
						}
					}
				}
			}
		}
	}

	// 3. Match <tool:name ...>args</tool:name>
	reToolColonAttrs := regexp.MustCompile(`(?s)<tool:([a-zA-Z0-9_\-]+)\b([^>]*)>(.*?)<\/tool:([a-zA-Z0-9_\-]+)>`)
	for _, match := range reToolColonAttrs.FindAllStringSubmatch(content, -1) {
		if len(match) >= 5 && match[1] == match[4] {
			toolName := match[1]
			attrs := ParseXMLAttrs(match[2])
			toolCalls = append(toolCalls, BuildXMLToolCall(toolName, attrs, match[3]))
		}
	}

	// 4. Match <execute name="name">args</execute>
	reExecuteAttrs := regexp.MustCompile(`(?s)<execute\b([^>]*)>(.*?)<\/execute>`)
	for _, match := range reExecuteAttrs.FindAllStringSubmatch(content, -1) {
		attrs := ParseXMLAttrs(match[1])
		name := attrs["name"]
		if name != "" {
			toolCalls = append(toolCalls, BuildXMLToolCall(name, attrs, match[2]))
		}
	}

	// 5. Bare Hermes functions if no tool calls parsed yet
	if len(toolCalls) == 0 {
		for _, match := range reFunc.FindAllStringSubmatch(content, -1) {
			if len(match) >= 3 {
				params := make(map[string]string)
				for _, p := range reParam.FindAllStringSubmatch(match[2], -1) {
					params[p[1]] = strings.TrimSpace(p[2])
				}
				if len(params) == 0 {
					toolCalls = append(toolCalls, BuildToolCall(match[1], strings.TrimSpace(match[2])))
				} else {
					args, err := json.Marshal(params)
					if err != nil {
						toolCalls = append(toolCalls, BuildToolCall(match[1], strings.TrimSpace(match[2])))
					} else {
						toolCalls = append(toolCalls, BuildToolCall(match[1], string(args)))
					}
				}
			}
		}
	}

	return toolCalls
}

// BuildToolCall normalizes tool names and formats arguments into a db.ToolCall.
func BuildToolCall(name string, args string) db.ToolCall {
	name = tool.NormalizeName(name)
	var temp map[string]interface{}
	isJSON := json.Unmarshal([]byte(args), &temp) == nil

	finalArgs := args
	if !isJSON {
		escapedArgs, _ := json.Marshal(args)
		escapedArgsStr := string(escapedArgs)

		switch {
		case name == "bash" || name == "ls":
			finalArgs = fmt.Sprintf(`{"command": %s}`, escapedArgsStr)
		case name == "read":
			finalArgs = fmt.Sprintf(`{"path": %s}`, escapedArgsStr)
		case name == "write":
			finalArgs = fmt.Sprintf(`{"content": %s}`, escapedArgsStr)
		case name == "edit":
			finalArgs = fmt.Sprintf(`{"content": %s}`, escapedArgsStr)
		case name == "grep":
			finalArgs = fmt.Sprintf(`{"pattern": %s}`, escapedArgsStr)
		case name == "load_skill":
			finalArgs = fmt.Sprintf(`{"name": %s}`, escapedArgsStr)
		case name == "task_status" || name == "task_kill":
			finalArgs = fmt.Sprintf(`{"task_id": %s}`, escapedArgsStr)
		case strings.HasPrefix(name, "subagent__"):
			finalArgs = fmt.Sprintf(`{"prompt": %s}`, escapedArgsStr)
		default:
			finalArgs = fmt.Sprintf(`{"arguments": %s}`, escapedArgsStr)
		}
	}

	tc := db.ToolCall{
		ID:   fmt.Sprintf("call_fallback_%s", db.NewUUID()[:8]),
		Type: "function",
	}
	tc.Function.Name = name
	tc.Function.Arguments = finalArgs
	return tc
}
