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

// ParseFallbackToolCalls extracts XML-style fallback tool calls from plain assistant message content.
func ParseFallbackToolCalls(content string) []db.ToolCall {
	var toolCalls []db.ToolCall

	// 1. Match <tool_call name="name">args</tool_call>
	appendNamedCalls(&toolCalls, reToolCallAttr, content)

	// 2. Match <tool:name>args</tool:name>
	for _, match := range reToolColon.FindAllStringSubmatch(content, -1) {
		if len(match) >= 4 && match[1] == match[3] {
			toolCalls = append(toolCalls, BuildToolCall(match[1], strings.TrimSpace(match[2])))
		}
	}

	// 3. Match <execute name="name">args</execute>
	appendNamedCalls(&toolCalls, reExecuteAttr, content)

	// 4. Match Hermes/Qwen chat-template dialect

	parseHermesFunction := func(name, body string) db.ToolCall {
		params := make(map[string]string)
		for _, p := range reParam.FindAllStringSubmatch(body, -1) {
			params[p[1]] = strings.TrimSpace(p[2])
		}
		if len(params) == 0 {
			return BuildToolCall(name, strings.TrimSpace(body))
		}
		args, err := json.Marshal(params)
		if err != nil {
			return BuildToolCall(name, strings.TrimSpace(body))
		}
		return BuildToolCall(name, string(args))
	}

	blocks := reBlock.FindAllStringSubmatch(content, -1)
	if len(blocks) > 0 {
		for _, match := range blocks {
			if len(match) < 2 {
				continue
			}
			body := match[1]
			for _, funcMatch := range reFunc.FindAllStringSubmatch(body, -1) {
				if len(funcMatch) >= 3 {
					toolCalls = append(toolCalls, parseHermesFunction(funcMatch[1], funcMatch[2]))
				}
			}
		}
	}

	if len(toolCalls) == 0 {
		for _, match := range reFunc.FindAllStringSubmatch(content, -1) {
			if len(match) >= 3 {
				toolCalls = append(toolCalls, parseHermesFunction(match[1], match[2]))
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
