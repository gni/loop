package fallback

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
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

// Reuse the executable tools' schemas so fallback XML cannot drift from their
// numeric, boolean and structured parameter contracts. Text stays literal.
var xmlArgumentSchemas = func() map[string]map[string]tool.SchemaProp {
	schemas := make(map[string]map[string]tool.SchemaProp)
	for _, executor := range []tool.ToolExecutor{
		tool.NewBashTool(), tool.NewReadTool(), tool.NewWriteTool(), tool.NewEditTool(),
		tool.NewFindTool(), tool.NewGrepTool(), tool.NewListTool(), tool.NewSymbolTool(),
		tool.NewTaskStatusTool(), tool.NewTaskKillTool(), tool.NewTodoTool(), tool.NewTaskStateTool(),
		tool.NewAskUserTool(), tool.NewLoadMCPTool(), tool.NewLoadSkillTool(),
	} {
		schemas[executor.Name()] = executor.Definition().Function.Parameters.Properties
	}
	return schemas
}()

func xmlArgumentValue(name, key, value string) interface{} {
	switch xmlArgumentSchemas[name][key].Type {
	case "number":
		if number, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
			return number
		}
	case "integer":
		if number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return number
		}
	case "boolean":
		if boolean, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return boolean
		}
	case "array":
		var array []interface{}
		if json.Unmarshal([]byte(value), &array) == nil && array != nil {
			return array
		}
	case "object":
		var object map[string]interface{}
		if json.Unmarshal([]byte(value), &object) == nil && object != nil {
			return object
		}
	}
	return value
}

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
		if temp == nil {
			temp = make(map[string]interface{})
		}
		for k, v := range attrs {
			if _, exists := temp[k]; !exists {
				temp[k] = xmlArgumentValue(name, k, v)
			}
		}
		if merged, err := json.Marshal(temp); err == nil {
			return BuildToolCall(name, string(merged))
		}
		return BuildToolCall(name, body)
	}

	argsMap := make(map[string]interface{})
	for k, v := range attrs {
		argsMap[k] = xmlArgumentValue(name, k, v)
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

	case "grep", "find_files":
		argsMap["pattern"] = tool.FirstNonEmpty(attrs["pattern"], body)

	case "list_directory":
		argsMap["path"] = tool.FirstNonEmpty(attrs["path"], body, ".")

	case "get_task_status", "stop_task":
		argsMap["task_id"] = tool.FirstNonEmpty(attrs["task_id"], attrs["id"], body)
	case "inspect_subagent", "remove_subagent", "load_skill", "load_mcp":
		argsMap["name"] = tool.FirstNonEmpty(attrs["name"], body)
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
		params := make(map[string]interface{})
		for _, p := range reParam.FindAllStringSubmatch(funcMatch[2], -1) {
			params[p[1]] = xmlArgumentValue(tool.NormalizeName(name), p[1], strings.TrimSpace(p[2]))
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
	type candidate struct {
		start, end int
		calls      []db.ToolCall
	}
	var found []candidate
	capture := func(match []int, index int) string {
		if index*2+1 >= len(match) || match[index*2] < 0 {
			return ""
		}
		return content[match[index*2]:match[index*2+1]]
	}
	add := func(match []int, calls ...db.ToolCall) {
		if len(calls) > 0 {
			found = append(found, candidate{match[0], match[1], calls})
		}
	}
	for _, direct := range compiledDirectTools {
		for _, match := range direct.selfClosing.FindAllStringSubmatchIndex(content, -1) {
			add(match, BuildXMLToolCall(direct.name, ParseXMLAttrs(capture(match, 1)), ""))
		}
		for _, match := range direct.paired.FindAllStringSubmatchIndex(content, -1) {
			add(match, BuildXMLToolCall(direct.name, ParseXMLAttrs(capture(match, 1)), capture(match, 2)))
		}
	}
	for _, match := range reToolCallBlock.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		body := capture(match, 2)
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, attrs, body))
		} else {
			add(match, parseHermesFunctionCalls(body)...)
		}
	}
	for _, match := range reToolColonAttrs.FindAllStringSubmatchIndex(content, -1) {
		if capture(match, 1) == capture(match, 4) {
			add(match, BuildXMLToolCall(capture(match, 1), ParseXMLAttrs(capture(match, 2)), capture(match, 3)))
		}
	}
	for _, match := range reExecuteAttrs.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, attrs, capture(match, 2)))
		}
	}
	for _, match := range reFunc.FindAllStringSubmatchIndex(content, -1) {
		add(match, parseHermesFunctionCalls(content[match[0]:match[1]])...)
	}
	if len(found) == 0 {
		return nil
	}
	// Tool families and aliases must not change execution order. Prefer an outer
	// wrapper over tool-looking text inside its arguments, avoiding duplicate calls.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].start == found[j].start {
			return found[i].end > found[j].end
		}
		return found[i].start < found[j].start
	})
	var calls []db.ToolCall
	end := -1
	for _, item := range found {
		if item.start < end {
			continue
		}
		calls = append(calls, item.calls...)
		end = item.end
	}
	return calls
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
		case name == "bash":
			finalArgs = fmt.Sprintf(`{"command": %s}`, escapedArgsStr)
		case name == "read" || name == "list_directory":
			finalArgs = fmt.Sprintf(`{"path": %s}`, escapedArgsStr)
		case name == "write":
			finalArgs = fmt.Sprintf(`{"content": %s}`, escapedArgsStr)
		case name == "edit":
			finalArgs = fmt.Sprintf(`{"content": %s}`, escapedArgsStr)
		case name == "grep" || name == "find_files":
			finalArgs = fmt.Sprintf(`{"pattern": %s}`, escapedArgsStr)
		case name == "load_skill" || name == "load_mcp" || name == "inspect_subagent" || name == "remove_subagent":
			finalArgs = fmt.Sprintf(`{"name": %s}`, escapedArgsStr)
		case name == "get_task_status" || name == "stop_task":
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
