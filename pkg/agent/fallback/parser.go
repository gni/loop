package fallback

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type directToolMatchers struct {
	name        string
	selfClosing *regexp.Regexp
	paired      *regexp.Regexp
}

var (
	reAttr           = regexp.MustCompile(`([a-zA-Z0-9_\-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>/]+))`)
	rePathTag        = regexp.MustCompile(`(?s)<path>(.*?)<\/path>`)
	reContentTag     = regexp.MustCompile(`(?s)<content>(.*?)<\/content>`)
	reInstructionTag = regexp.MustCompile(`(?s)<(?:instructions?|description)>(.*?)<\/(?:instructions?|description)>`)
	oldNewTagRegexes = []*regexp.Regexp{
		regexp.MustCompile(`(?s)<old>(.*?)<\/old>\s*<new>(.*?)<\/new>`),
		regexp.MustCompile(`(?s)<oldText>(.*?)<\/oldText>\s*<newText>(.*?)<\/newText>`),
		regexp.MustCompile(`(?s)<old_text>(.*?)<\/old_text>\s*<new_text>(.*?)<\/new_text>`),
	}
	reConflictBlock     = regexp.MustCompile(`(?s)<<<<<<< SEARCH\r?\n(.*?)\r?\n=======\r?\n(.*?)\r?\n>>>>>>> REPLACE`)
	reTaskTag           = regexp.MustCompile(`(?s)<(?:task|item)(?:\s+([^>]*))?>\s*(.*?)\s*<\/(?:task|item)>`)
	reQuestionTag       = regexp.MustCompile(`(?s)<question>(.*?)<\/question>`)
	reOptionTag         = regexp.MustCompile(`(?s)<option(?:\s+([^>]*))?>(.*?)<\/option>`)
	reInlineSkillTag    = regexp.MustCompile(`(?s)<skill(?:\s+([^>]*))?>(.*?)<\/skill>`)
	reDynamicSelfClose  = regexp.MustCompile(`(?s)<((?:subagent|mcp|plugin)__[a-zA-Z0-9_\-]+)\b(` + xmlAttrsPattern + `)\/>`)
	reDynamicPaired     = regexp.MustCompile(`(?s)<((?:subagent|mcp|plugin)__[a-zA-Z0-9_\-]+)\b(` + xmlPairedAttrsPattern + `)>(.*?)<\/((?:subagent|mcp|plugin)__[a-zA-Z0-9_\-]+)>`)
	reToolCallSelfClose = regexp.MustCompile(`(?s)<tool_call\b(` + xmlAttrsPattern + `)\/>`)
	reToolCallBlock     = regexp.MustCompile(`(?s)<tool_call\b(` + xmlPairedAttrsPattern + `)>(.*?)<\/tool_call>`)
	reToolColonSelf     = regexp.MustCompile(`(?s)<tool:([a-zA-Z0-9_\-]+)\b(` + xmlAttrsPattern + `)\/>`)
	reToolColonAttrs    = regexp.MustCompile(`(?s)<tool:([a-zA-Z0-9_\-]+)\b(` + xmlPairedAttrsPattern + `)>(.*?)<\/tool:([a-zA-Z0-9_\-]+)>`)
	reExecuteSelf       = regexp.MustCompile(`(?s)<execute\b(` + xmlAttrsPattern + `)\/>`)
	reExecuteAttrs      = regexp.MustCompile(`(?s)<execute\b(` + xmlPairedAttrsPattern + `)>(.*?)<\/execute>`)
	reFunc              = regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-]+)\s*>(.*?)<\/function\s*>`)
	reParam             = regexp.MustCompile(`(?s)<parameter(?:\s+name\s*=\s*(?:"([a-zA-Z0-9_\-]+)"|'([a-zA-Z0-9_\-]+)'|([a-zA-Z0-9_\-]+))|[=\s]+([a-zA-Z0-9_\-]+))\s*>(.*?)<\/parameter\s*>`)

	compiledDirectTools = func() []directToolMatchers {
		out := make([]directToolMatchers, len(directToolNames))
		for i, name := range directToolNames {
			out[i] = directToolMatchers{
				name:        name,
				selfClosing: regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b(%s)\/>`, name, xmlAttrsPattern)),
				paired:      regexp.MustCompile(fmt.Sprintf(`(?s)<%s\b(%s)>(.*?)<\/%s>`, name, xmlPairedAttrsPattern, name)),
			}
		}
		return out
	}()
)

const xmlAttrsPattern = `(?:[^"'<>]|"[^"]*"|'[^']*')*?`
const xmlPairedAttrsPattern = `(?:(?:[^"'<>]|"[^"]*"|'[^']*')*?(?:[^"'<>/]|"[^"]*"|'[^']*'))?`

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

func xmlArgumentValue(name, key, value string) any {
	switch xmlArgumentSchemas[name][key].Type {
	case "number":
		trimmed := strings.TrimSpace(value)
		if number, err := strconv.ParseFloat(trimmed, 64); err == nil && !math.IsNaN(number) && !math.IsInf(number, 0) {
			if json.Valid([]byte(trimmed)) {
				return json.Number(trimmed)
			}
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
		decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(value))
		if array, ok := decoded.([]any); err == nil && ok {
			return array
		}
	case "object":
		decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(value))
		if object, ok := decoded.(map[string]any); err == nil && ok {
			return object
		}
	}
	return value
}

// trimTagBoundaryNewlines removes at most one leading and one trailing newline
// immediately adjacent to XML tags while preserving indentation and blank lines.
func trimTagBoundaryNewlines(s string) string {
	s = strings.TrimPrefix(s, "\r\n")
	if !strings.HasPrefix(s, "\r\n") {
		s = strings.TrimPrefix(s, "\n")
	}
	s = strings.TrimSuffix(s, "\r\n")
	if !strings.HasSuffix(s, "\r\n") {
		s = strings.TrimSuffix(s, "\n")
	}
	return s
}

func extractParameterTags(body string) (map[string]string, bool) {
	matches := reParam.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, false
	}
	params := make(map[string]string, len(matches))
	for _, m := range matches {
		key := tool.FirstNonEmpty(m[1], m[2], m[3], m[4])
		if key != "" {
			params[key] = trimTagBoundaryNewlines(m[5])
		}
	}
	return params, len(params) > 0
}

func stripWrapperNameAttr(attrs map[string]string) map[string]string {
	if _, ok := attrs["name"]; !ok {
		return attrs
	}
	cleaned := make(map[string]string, len(attrs)-1)
	for k, v := range attrs {
		if k != "name" {
			cleaned[k] = v
		}
	}
	return cleaned
}

// ParseXMLAttrs extracts key="val", key='val', or key=val attribute pairs from XML tag string.
func ParseXMLAttrs(s string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		raw := tool.FirstNonEmpty(m[2], m[3], m[4])
		attrs[m[1]] = html.UnescapeString(raw)
	}
	return attrs
}

// BuildXMLToolCall constructs a structured db.ToolCall from an XML tag and its body.
func BuildXMLToolCall(name string, attrs map[string]string, rawBody string) db.ToolCall {
	name = tool.NormalizeName(name)
	trimmedBody := strings.TrimSpace(rawBody)

	// If body is already valid JSON object, merge tag attributes into it if not present.
	decoded, decodeErr := jsonschema.UnmarshalJSON(strings.NewReader(trimmedBody))
	temp, isObject := decoded.(map[string]any)
	if decodeErr == nil && (isObject || decoded == nil) {
		if temp == nil {
			temp = make(map[string]any)
		}
		for k, v := range attrs {
			if _, exists := temp[k]; !exists {
				temp[k] = xmlArgumentValue(name, k, v)
			}
		}
		if merged, err := json.Marshal(temp); err == nil {
			return BuildToolCall(name, string(merged))
		}
		return BuildToolCall(name, trimmedBody)
	}

	argsMap := make(map[string]any)
	for k, v := range attrs {
		argsMap[k] = xmlArgumentValue(name, k, v)
	}

	// Support <parameter name="key">value</parameter> or <parameter=key>value</parameter>
	// inside any tool tag (including custom, plugin, and MCP tools).
	if params, hasParams := extractParameterTags(rawBody); hasParams {
		for k, v := range params {
			argsMap[k] = xmlArgumentValue(name, k, v)
		}
		if jsonBytes, err := json.Marshal(argsMap); err == nil {
			return BuildToolCall(name, string(jsonBytes))
		}
	}

	switch name {
	case "write":
		path := tool.FirstNonEmpty(attrs["path"], attrs["file"], attrs["target"])
		if path == "" {
			if reP := rePathTag.FindStringSubmatch(rawBody); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		content := trimTagBoundaryNewlines(rawBody)
		if reC := reContentTag.FindStringSubmatch(rawBody); len(reC) >= 2 {
			content = trimTagBoundaryNewlines(reC[1])
		}
		argsMap["path"] = path
		argsMap["content"] = content

	case "edit":
		path := tool.FirstNonEmpty(attrs["path"], attrs["file"])
		if path == "" {
			if reP := rePathTag.FindStringSubmatch(rawBody); len(reP) >= 2 {
				path = strings.TrimSpace(reP[1])
			}
		}
		argsMap["path"] = path

		instructions := tool.FirstNonEmpty(attrs["instructions"], attrs["instruction"], attrs["description"])
		if instructions == "" {
			if reI := reInstructionTag.FindStringSubmatch(rawBody); len(reI) >= 2 {
				instructions = strings.TrimSpace(reI[1])
			}
		}
		if instructions != "" {
			argsMap["instructions"] = instructions
		}

		var oldNewMatches [][]string
		for _, re := range oldNewTagRegexes {
			if m := re.FindAllStringSubmatch(rawBody, -1); len(m) > 0 {
				oldNewMatches = append(oldNewMatches, m...)
			}
		}
		fromOldNewTags := len(oldNewMatches) > 0
		if !fromOldNewTags {
			oldNewMatches = reConflictBlock.FindAllStringSubmatch(rawBody, -1)
		}
		if len(oldNewMatches) > 0 {
			updates := make([]map[string]string, 0, len(oldNewMatches))
			for _, m := range oldNewMatches {
				oldText, newText := m[1], m[2]
				if fromOldNewTags {
					oldText = trimTagBoundaryNewlines(oldText)
					newText = trimTagBoundaryNewlines(newText)
				}
				updates = append(updates, map[string]string{
					"oldText": oldText,
					"newText": newText,
				})
			}
			argsMap["updates"] = updates
		} else if trimmedBody != "" {
			argsMap["content"] = trimTagBoundaryNewlines(rawBody)
		}

	case "bash":
		cmd := tool.FirstNonEmpty(attrs["command"], trimTagBoundaryNewlines(rawBody))
		if !strings.Contains(cmd, "\n") {
			cmd = strings.TrimSpace(cmd)
		}
		argsMap["command"] = cmd
		if b := attrs["background"]; b == "true" || b == "1" {
			argsMap["background"] = true
		}

	case "read":
		argsMap["path"] = tool.FirstNonEmpty(attrs["path"], trimmedBody)

	case "grep", "find_files":
		argsMap["pattern"] = tool.FirstNonEmpty(attrs["pattern"], trimmedBody)

	case "list_directory":
		argsMap["path"] = tool.FirstNonEmpty(attrs["path"], trimmedBody, ".")

	case "search_tools":
		argsMap["query"] = tool.FirstNonEmpty(attrs["query"], trimmedBody)

	case "search_symbols":
		argsMap["query"] = tool.FirstNonEmpty(attrs["query"], attrs["name"], trimmedBody)

	case "get_task_status", "stop_task":
		argsMap["task_id"] = tool.FirstNonEmpty(attrs["task_id"], attrs["id"], trimmedBody)

	case "inspect_subagent", "remove_subagent", "load_mcp":
		argsMap["name"] = tool.FirstNonEmpty(attrs["name"], trimmedBody)

	case "load_skill":
		if name := tool.FirstNonEmpty(attrs["name"], trimmedBody); name != "" {
			argsMap["name"] = name
		}

	case "read_mcp_resource":
		if uri := tool.FirstNonEmpty(attrs["uri"], trimmedBody); uri != "" {
			argsMap["uri"] = uri
		}

	case "create_subagent":
		bodyWithoutSkills := trimmedBody
		if skillMatches := reInlineSkillTag.FindAllStringSubmatch(rawBody, -1); len(skillMatches) > 0 {
			inlineSkills := make([]map[string]string, 0, len(skillMatches))
			for _, sm := range skillMatches {
				sAttrs := ParseXMLAttrs(sm[1])
				sName := strings.TrimSpace(sAttrs["name"])
				sInstr := trimTagBoundaryNewlines(sm[2])
				if sName != "" && strings.TrimSpace(sInstr) != "" {
					entry := map[string]string{
						"name":         sName,
						"instructions": sInstr,
					}
					if desc := strings.TrimSpace(sAttrs["description"]); desc != "" {
						entry["description"] = desc
					}
					inlineSkills = append(inlineSkills, entry)
				}
			}
			if len(inlineSkills) > 0 {
				argsMap["inline_skills"] = inlineSkills
			}
			bodyWithoutSkills = strings.TrimSpace(reInlineSkillTag.ReplaceAllString(rawBody, ""))
		}
		if _, ok := argsMap["system_prompt"]; !ok {
			if sp := tool.FirstNonEmpty(attrs["system_prompt"], attrs["instructions"], attrs["prompt"], attrs["role"], bodyWithoutSkills); sp != "" {
				argsMap["system_prompt"] = sp
			}
		}

	case "ask_user":
		if q := reQuestionTag.FindStringSubmatch(rawBody); len(q) >= 2 {
			argsMap["question"] = strings.TrimSpace(q[1])
		} else if _, ok := argsMap["question"]; !ok && trimmedBody != "" && !strings.Contains(rawBody, "<option") {
			argsMap["question"] = trimmedBody
		}
		if optMatches := reOptionTag.FindAllStringSubmatch(rawBody, -1); len(optMatches) > 0 {
			options := make([]map[string]any, 0, len(optMatches))
			for _, om := range optMatches {
				oAttrs := ParseXMLAttrs(om[1])
				label := tool.FirstNonEmpty(oAttrs["label"], strings.TrimSpace(om[2]))
				if label == "" {
					continue
				}
				entry := map[string]any{"label": label}
				if desc := strings.TrimSpace(oAttrs["description"]); desc != "" {
					entry["description"] = desc
				} else if oAttrs["label"] != "" && strings.TrimSpace(om[2]) != "" {
					entry["description"] = strings.TrimSpace(om[2])
				}
				if rec := strings.ToLower(strings.TrimSpace(oAttrs["recommended"])); rec == "true" || rec == "1" {
					entry["recommended"] = true
					if _, hasRec := argsMap["recommended"]; !hasRec {
						argsMap["recommended"] = label
					}
				}
				options = append(options, entry)
			}
			if len(options) > 0 {
				argsMap["options"] = options
			}
		}

	case "task_state":
		if _, ok := argsMap["summary"]; !ok && trimmedBody != "" {
			argsMap["summary"] = trimmedBody
		}

	case "todo":
		if taskMatches := reTaskTag.FindAllStringSubmatch(rawBody, -1); len(taskMatches) > 0 {
			var tasks []map[string]string
			for idx, tm := range taskMatches {
				tAttrs := ParseXMLAttrs(tm[1])
				id := tool.FirstNonEmpty(tAttrs["id"], fmt.Sprintf("task-%d", idx+1))
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
		} else if trimmedBody != "" {
			argsMap["markdown"] = trimTagBoundaryNewlines(rawBody)
		}

	default:
		if strings.HasPrefix(name, "subagent__") {
			if _, ok := argsMap["prompt"]; !ok && trimmedBody != "" {
				argsMap["prompt"] = trimmedBody
			}
		} else if len(argsMap) == 0 && trimmedBody != "" {
			return BuildToolCall(name, trimmedBody)
		}
	}

	jsonBytes, err := json.Marshal(argsMap)
	if err != nil {
		return BuildToolCall(name, trimmedBody)
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
		params := make(map[string]any)
		for _, p := range reParam.FindAllStringSubmatch(funcMatch[2], -1) {
			key := tool.FirstNonEmpty(p[1], p[2], p[3], p[4])
			val := trimTagBoundaryNewlines(p[5])
			if key != "" {
				params[key] = xmlArgumentValue(tool.NormalizeName(name), key, val)
			}
		}
		if strings.Contains(rawBody, "<parameter") && strings.TrimSpace(reParam.ReplaceAllString(rawBody, "")) != "" {
			continue
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
	calls, _ := ParseFallbackToolCallsStrict(content)
	return calls
}

const MaxToolCalls = 1024

// ParseFallbackToolCallsStrict bounds parsing work and reports oversized batches
// instead of silently executing a truncated subset of the model's calls.
func ParseFallbackToolCallsStrict(content string) ([]db.ToolCall, error) {
	openings := 0
	for _, marker := range fallbackOpeningMarkers {
		openings += strings.Count(content, marker)
		if openings > MaxToolCalls*4 {
			return nil, fmt.Errorf("fallback response exceeds tool call limit")
		}
	}
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
	for _, match := range reDynamicSelfClose.FindAllStringSubmatchIndex(content, -1) {
		add(match, BuildXMLToolCall(capture(match, 1), ParseXMLAttrs(capture(match, 2)), ""))
	}
	for _, match := range reDynamicPaired.FindAllStringSubmatchIndex(content, -1) {
		if capture(match, 1) == capture(match, 4) {
			add(match, BuildXMLToolCall(capture(match, 1), ParseXMLAttrs(capture(match, 2)), capture(match, 3)))
		}
	}
	for _, match := range reToolCallSelfClose.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, stripWrapperNameAttr(attrs), ""))
		}
	}
	for _, match := range reToolCallBlock.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		body := capture(match, 2)
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, stripWrapperNameAttr(attrs), body))
		} else {
			add(match, parseHermesFunctionCalls(body)...)
		}
	}
	for _, match := range reToolColonSelf.FindAllStringSubmatchIndex(content, -1) {
		add(match, BuildXMLToolCall(capture(match, 1), ParseXMLAttrs(capture(match, 2)), ""))
	}
	for _, match := range reToolColonAttrs.FindAllStringSubmatchIndex(content, -1) {
		if capture(match, 1) == capture(match, 4) {
			add(match, BuildXMLToolCall(capture(match, 1), ParseXMLAttrs(capture(match, 2)), capture(match, 3)))
		}
	}
	for _, match := range reExecuteSelf.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, stripWrapperNameAttr(attrs), ""))
		}
	}
	for _, match := range reExecuteAttrs.FindAllStringSubmatchIndex(content, -1) {
		attrs := ParseXMLAttrs(capture(match, 1))
		if name := attrs["name"]; name != "" {
			add(match, BuildXMLToolCall(name, stripWrapperNameAttr(attrs), capture(match, 2)))
		}
	}
	for _, match := range reFunc.FindAllStringSubmatchIndex(content, -1) {
		add(match, parseHermesFunctionCalls(content[match[0]:match[1]])...)
	}
	if len(found) == 0 {
		return nil, nil
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
		if len(calls) > MaxToolCalls {
			return nil, fmt.Errorf("fallback response exceeds %d tool calls", MaxToolCalls)
		}
		end = item.end
	}
	return calls, nil
}

// BuildToolCall normalizes tool names and formats arguments into a db.ToolCall.
func BuildToolCall(name string, args string) db.ToolCall {
	name = tool.NormalizeName(name)
	trimmed := strings.TrimSpace(args)
	isJSON := json.Valid([]byte(trimmed)) && (strings.HasPrefix(trimmed, "{") || trimmed == "null")

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
