package fallback

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	domaintool "loop/pkg/domain/tool"
)

type directToolMatchers struct {
	name        string
	selfClosing *regexp.Regexp
	paired      *regexp.Regexp
}

var (
	reAttr           = regexp.MustCompile(`([a-zA-Z0-9_\-]+)=(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	rePathTag        = regexp.MustCompile(`(?s)<path>(.*?)<\/path>`)
	reContentTag     = regexp.MustCompile(`(?s)<content>(.*?)<\/content>`)
	reInstructionTag = regexp.MustCompile(`(?s)<(?:instructions?|description)>(.*?)<\/(?:instructions?|description)>`)
	reOldNewTag      = regexp.MustCompile(`(?s)<old>(.*?)<\/old>\s*<new>(.*?)<\/new>`)
	reConflictBlock  = regexp.MustCompile(`(?s)<<<<<<< SEARCH\r?\n(.*?)\r?\n=======\r?\n(.*?)\r?\n>>>>>>> REPLACE`)
	reTaskTag        = regexp.MustCompile(`(?s)<(?:task|item)(?:\s+([^>]*))?>\s*(.*?)\s*<\/(?:task|item)>`)
	reToolCallBlock  = regexp.MustCompile(`(?s)<tool_call\b([^>]*)>(.*?)<\/tool_call>`)
	reToolColonAttrs = regexp.MustCompile(`(?s)<tool:([a-zA-Z0-9_\-]+)\b([^>]*)>(.*?)<\/tool:([a-zA-Z0-9_\-]+)>`)
	reExecuteAttrs   = regexp.MustCompile(`(?s)<execute\b([^>]*)>(.*?)<\/execute>`)
	reFunc           = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/function(?:\s*>|\s*\n|$)|$)`)
	reParam          = regexp.MustCompile(`(?s)<parameter[=\s]*([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/parameter(?:\s*>|\s*\n|$)|$)`)

	compiledDirectTools = func() []directToolMatchers {
		out := make([]directToolMatchers, len(directToolNames))
		for i, name := range directToolNames {
			out[i] = directToolMatchers{
				name:        name,
				selfClosing: regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b([^>]*?)\/>`, name)),
				paired:      regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b([^>]*?)>(.*?)<\/%s>`, name, name)),
			}
		}
		return out
	}()
)

// ParseXMLAttrs extracts key="val", key='val', or key=val attribute pairs from XML tag string.
func ParseXMLAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		attrs[m[1]] = tool.FirstNonEmpty(m[2], m[3], m[4])
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
		path := tool.FirstNonEmpty(attrs["path"], attrs["file"], attrs["target"])
		if path == "" {
			if reP := rePathTag.FindStringSubmatch(body); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		content := body
		if reC := reContentTag.FindStringSubmatch(body); len(reC) >= 2 {
			content = reC[1]
		}
		argsMap["path"] = path
		argsMap["content"] = content

	case "edit":
		path := tool.FirstNonEmpty(attrs["path"], attrs["file"])
		if path == "" {
			if reP := rePathTag.FindStringSubmatch(body); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		argsMap["path"] = path

		instructions := tool.FirstNonEmpty(attrs["instructions"], attrs["instruction"], attrs["description"])
		if instructions == "" {
			if reI := reInstructionTag.FindStringSubmatch(body); len(reI) >= 2 {
				instructions = strings.TrimSpace(reI[1])
			}
		}
		if instructions != "" {
			argsMap["instructions"] = instructions
		}

		oldNewMatches := reOldNewTag.FindAllStringSubmatch(body, -1)
		if len(oldNewMatches) == 0 {
			oldNewMatches = reConflictBlock.FindAllStringSubmatch(body, -1)
		}
		if len(oldNewMatches) > 0 {
			updates := make([]map[string]string, 0, len(oldNewMatches))
			for _, m := range oldNewMatches {
				updates = append(updates, map[string]string{
					"oldText": m[1],
					"newText": m[2],
				})
			}
			argsMap["updates"] = updates
		} else if body != "" {
			argsMap["content"] = body
		}

	case "bash":
		argsMap["command"] = tool.FirstNonEmpty(attrs["command"], body)
		if b := attrs["background"]; b == "true" || b == "1" {
			argsMap["background"] = true
		}

	case "read":
		argsMap["path"] = tool.FirstNonEmpty(attrs["path"], body)

	case "grep", "find":
		argsMap["pattern"] = tool.FirstNonEmpty(attrs["pattern"], body)

	case "list":
		argsMap["path"] = tool.FirstNonEmpty(attrs["path"], body, ".")

	case "todo":
		if taskMatches := reTaskTag.FindAllStringSubmatch(body, -1); len(taskMatches) > 0 {
			var tasks []map[string]string
			for idx, tm := range taskMatches {
				tAttrs := ParseXMLAttrs(tm[1])
				id := tool.FirstNonEmpty(tAttrs["id"], fmt.Sprintf("task_%d", idx+1))
				status := tool.FirstNonEmpty(tAttrs["status"], "pending")
				taskDesc := tool.FirstNonEmpty(strings.TrimSpace(tm[2]), tAttrs["task"], tAttrs["content"], tAttrs["description"])
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
		} else if tasks := domaintool.ParseMarkdownTodoLines(body, "task_"); len(tasks) > 0 {
			argsMap["tasks"] = tasks
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

func parseHermesFunctionCalls(body string) []db.ToolCall {
	var calls []db.ToolCall
	for _, funcMatch := range reFunc.FindAllStringSubmatch(body, -1) {
		if len(funcMatch) < 3 {
			continue
		}
		name := funcMatch[1]
		rawBody := strings.TrimSpace(funcMatch[2])
		params := make(map[string]string)
		for _, p := range reParam.FindAllStringSubmatch(funcMatch[2], -1) {
			params[p[1]] = strings.TrimSpace(p[2])
		}
		if len(params) == 0 {
			calls = append(calls, BuildToolCall(name, rawBody))
			continue
		}
		if args, err := json.Marshal(params); err == nil {
			calls = append(calls, BuildToolCall(name, string(args)))
		} else {
			calls = append(calls, BuildToolCall(name, rawBody))
		}
	}
	return calls
}

// ParseFallbackToolCalls extracts XML-style fallback tool calls from plain assistant message content.
func ParseFallbackToolCalls(content string) []db.ToolCall {
	var toolCalls []db.ToolCall

	// 1. Direct tool tags: <write path="...">content</write>, <edit path="...">content</edit>, <bash>cmd</bash>, etc.
	for _, dt := range compiledDirectTools {
		for _, match := range dt.selfClosing.FindAllStringSubmatch(content, -1) {
			attrs := ParseXMLAttrs(match[1])
			toolCalls = append(toolCalls, BuildXMLToolCall(dt.name, attrs, ""))
		}
		for _, match := range dt.paired.FindAllStringSubmatch(content, -1) {
			attrs := ParseXMLAttrs(match[1])
			toolCalls = append(toolCalls, BuildXMLToolCall(dt.name, attrs, match[2]))
		}
	}

	// 2. Match <tool_call ...>args</tool_call>
	for _, match := range reToolCallBlock.FindAllStringSubmatch(content, -1) {
		attrs := ParseXMLAttrs(match[1])
		if name := attrs["name"]; name != "" {
			toolCalls = append(toolCalls, BuildXMLToolCall(name, attrs, match[2]))
		} else {
			toolCalls = append(toolCalls, parseHermesFunctionCalls(match[2])...)
		}
	}

	// 3. Match <tool:name ...>args</tool:name>
	for _, match := range reToolColonAttrs.FindAllStringSubmatch(content, -1) {
		if len(match) >= 5 && match[1] == match[4] {
			attrs := ParseXMLAttrs(match[2])
			toolCalls = append(toolCalls, BuildXMLToolCall(match[1], attrs, match[3]))
		}
	}

	// 4. Match <execute name="name">args</execute>
	for _, match := range reExecuteAttrs.FindAllStringSubmatch(content, -1) {
		attrs := ParseXMLAttrs(match[1])
		if name := attrs["name"]; name != "" {
			toolCalls = append(toolCalls, BuildXMLToolCall(name, attrs, match[2]))
		}
	}

	// 5. Bare Hermes functions if no tool calls parsed yet
	if len(toolCalls) == 0 {
		toolCalls = append(toolCalls, parseHermesFunctionCalls(content)...)
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
