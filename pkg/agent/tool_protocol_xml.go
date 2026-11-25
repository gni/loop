package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"sort"
	"strings"

	"loop/pkg/agent/fallback"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	domaintool "loop/pkg/domain/tool"
)

// xmlToolProtocol implements text-tag XML tool calling: full tool specs in the
// system prompt, XML-encoded assistant tool calls and <tool_result> user turns
// on the wire (without native JSON tools payloads), streaming XML tag stripping,
// and strict XML response parsing.
type xmlToolProtocol struct{}

func (xmlToolProtocol) Name() string                  { return "xml" }
func (xmlToolProtocol) UsesNativeSchemas() bool       { return false }
func (xmlToolProtocol) CompactToolSearchResult() bool { return false }

func (xmlToolProtocol) SelectInferenceTools(_ *Agent, _ context.Context, tools []tool.Tool, _ []string) []tool.Tool {
	return tools
}

func (xmlToolProtocol) PrepareWireTools(_ []tool.Tool, _ bool) []tool.Tool {
	return nil
}

// PrepareWireMessages translates canonical assistant ToolCalls and role="tool"
// results into XML-tagged text turns so providers without native tool schemas
// (and strict APIs like Anthropic/Gemini/OpenAI when tools=nil) accept multi-turn
// XML conversations cleanly without mutating stored session messages.
func (xmlToolProtocol) PrepareWireMessages(messages []db.Message) []db.Message {
	paired := EnforceToolPairingInvariance(messages)
	out := make([]db.Message, 0, len(paired))
	for _, msg := range paired {
		switch msg.Role {
		case "assistant":
			wire := msg
			if len(msg.ToolCalls) > 0 {
				var sb strings.Builder
				if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
					sb.WriteString(trimmed)
					sb.WriteString("\n\n")
				}
				for i, call := range msg.ToolCalls {
					if i > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(formatXMLToolCall(call))
				}
				wire.Content = sb.String()
				wire.ToolCalls = nil
			}
			out = append(out, wire)

		case "tool":
			wire := db.Message{
				Role:          "user",
				Content:       formatXMLToolResult(msg),
				ContentBlocks: msg.ContentBlocks,
				CacheControl:  msg.CacheControl,
			}
			out = append(out, wire)

		default:
			out = append(out, msg)
		}
	}
	return out
}

func (xmlToolProtocol) FormatPromptTools(sb *strings.Builder, entries []ToolEntry, _ map[string]bool, cfg SystemPromptConfig) {
	t := tpl()
	agentTpl := domaintool.AgentTemplatesSnapshot()
	var toolLines strings.Builder
	for _, entry := range entries {
		sig := formatXMLToolSignature(entry)
		toolLines.WriteString(fmt.Sprintf("- %s: %s\n", sig, entry.Snippet))
	}
	for _, name := range cfg.ActiveAgents {
		toolName := "subagent__" + name
		if !tool.IsAllowedTool(toolName, cfg.Allowlist) {
			continue
		}
		toolLines.WriteString(fmt.Sprintf(t.SubagentLine, name, fmt.Sprintf(agentTpl.SubagentPrompt, name)))
	}
	if toolLines.Len() == 0 {
		return
	}
	sb.WriteString(fmt.Sprintf(t.ToolsSection, toolLines.String()))
	sb.WriteString("<tool_protocol>\n")
	sb.WriteString("Call tools using XML tags directly in your response (do not wrap tool tags in markdown code fences). Tool outputs return inside <tool_result name=\"...\">...</tool_result>.\n")
	sb.WriteString("Examples:\n")
	sb.WriteString("<read path=\"pkg/main.go\" offset=\"1\" limit=\"80\"/>\n")
	sb.WriteString("<write path=\"pkg/file.go\">\npackage main\n</write>\n")
	sb.WriteString("<edit path=\"pkg/file.go\">\n<old>\nold line\n</old>\n<new>\nnew line\n</new>\n</edit>\n")
	sb.WriteString("<bash>go vet ./...</bash>\n")
	sb.WriteString("<todo>\n- [>] task-1: Inspect files\n- [ ] task-2: Apply fix\n</todo>\n")
	sb.WriteString("<todo action=\"update\" task_id=\"task-1\" status=\"completed\"/>\n")
	sb.WriteString("For any other tool, pass scalar arguments as attributes or child <parameter name=\"key\">value</parameter> tags (e.g. <tool_call name=\"tool_name\" arg=\"val\"/>).\n")
	sb.WriteString("</tool_protocol>\n\n")
}

func (xmlToolProtocol) NewStreamTextFilter(emit func(string), onToolName func(string, int), onToolTarget func(string, int), onToolCall func(string, int)) *FallbackToolTextFilter {
	filter := fallback.NewToolTextFilter(emit)
	if onToolName != nil || onToolCall != nil {
		filter.SetToolCallbacks(
			func(toolName string, idx int) {
				if onToolName != nil {
					onToolName(tool.NormalizeName(toolName), idx)
				}
			},
			func(chunk string, idx int) {
				if onToolCall != nil {
					onToolCall(chunk, idx)
				}
			},
		)
	}
	if onToolTarget != nil {
		filter.SetToolTargetCallback(onToolTarget)
	}
	return filter
}

func (xmlToolProtocol) ParseAssistantToolCalls(rawText string, nativeCalls []db.ToolCall) ([]db.ToolCall, string, error) {
	if len(nativeCalls) > 0 {
		return nativeCalls, rawText, nil
	}
	calls, err := fallback.ParseFallbackToolCallsStrict(rawText)
	if err != nil {
		return nil, rawText, err
	}
	return calls, fallback.StripFallbackToolMarkup(rawText), nil
}

func formatXMLToolSignature(entry ToolEntry) string {
	props := entry.Parameters.Properties
	if len(props) == 0 {
		return entry.Name
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		p := props[k]
		kind := p.Type
		if len(p.Enum) > 0 {
			kind = strings.Join(p.Enum, "|")
		}
		if kind == "" {
			kind = "any"
		}
		mark := ""
		if !slices.Contains(entry.Parameters.Required, k) {
			mark = "?"
		}
		parts = append(parts, fmt.Sprintf("%s%s=%s", k, mark, kind))
	}
	return fmt.Sprintf("%s(%s)", entry.Name, strings.Join(parts, ", "))
}

func escapeXMLAttr(v string) string {
	return html.EscapeString(v)
}

func xmlAttrString(key string, val any) string {
	if val == nil {
		return ""
	}
	var s string
	switch v := val.(type) {
	case string:
		s = v
	case float64, int, int64, bool, json.Number:
		s = fmt.Sprintf("%v", v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		s = string(b)
	}
	return fmt.Sprintf(` %s="%s"`, key, escapeXMLAttr(s))
}

func formatXMLToolResult(msg db.Message) string {
	name := tool.NormalizeName(msg.Name)
	if name == "" {
		name = "tool"
	}
	errAttr := ""
	if msg.IsError {
		errAttr = ` error="true"`
	}
	body := msg.Content
	if body == "" {
		if len(msg.ContentBlocks) > 0 {
			body = "(see attached image)"
		} else {
			body = "(no output)"
		}
	}
	return fmt.Sprintf("<tool_result name=\"%s\"%s>\n%s\n</tool_result>", escapeXMLAttr(name), errAttr, body)
}

func formatXMLToolCall(call db.ToolCall) string {
	name := tool.NormalizeName(call.Function.Name)
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
		return fmt.Sprintf("<tool_call name=\"%s\">\n%s\n</tool_call>", escapeXMLAttr(name), call.Function.Arguments)
	}

	switch name {
	case "write":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return fmt.Sprintf("<write path=\"%s\">\n%s\n</write>", escapeXMLAttr(path), content)

	case "edit":
		path, _ := args["path"].(string)
		var sb strings.Builder
		fmt.Fprintf(&sb, "<edit path=\"%s\">\n", escapeXMLAttr(path))
		if updates, ok := args["updates"].([]any); ok && len(updates) > 0 {
			for _, u := range updates {
				if m, ok := u.(map[string]any); ok {
					oldText, _ := m["oldText"].(string)
					newText, _ := m["newText"].(string)
					fmt.Fprintf(&sb, "<old>\n%s\n</old>\n<new>\n%s\n</new>\n", oldText, newText)
				}
			}
		} else if oldText, ok := args["oldText"].(string); ok {
			newText, _ := args["newText"].(string)
			fmt.Fprintf(&sb, "<old>\n%s\n</old>\n<new>\n%s\n</new>\n", oldText, newText)
		} else if content, ok := args["content"].(string); ok {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		sb.WriteString("</edit>")
		return sb.String()

	case "bash":
		cmd, _ := args["command"].(string)
		var attrs strings.Builder
		for _, k := range []string{"dir", "background", "timeout"} {
			if v, ok := args[k]; ok {
				attrs.WriteString(xmlAttrString(k, v))
			}
		}
		return fmt.Sprintf("<bash%s>\n%s\n</bash>", attrs.String(), cmd)

	case "todo":
		if md, ok := args["markdown"].(string); ok && strings.TrimSpace(md) != "" {
			return fmt.Sprintf("<todo>\n%s\n</todo>", strings.TrimSpace(md))
		}
		if tasks, ok := args["tasks"].([]any); ok && len(tasks) > 0 {
			var sb strings.Builder
			sb.WriteString("<todo>\n")
			for _, item := range tasks {
				if m, ok := item.(map[string]any); ok {
					id, desc, status := domaintool.ExtractTodoFields(m)
					marker := "[ ]"
					switch status {
					case "completed":
						marker = "[x]"
					case "in_progress":
						marker = "[>]"
					}
					if id != "" {
						fmt.Fprintf(&sb, "- %s %s: %s\n", marker, id, desc)
					} else {
						fmt.Fprintf(&sb, "- %s %s\n", marker, desc)
					}
				}
			}
			sb.WriteString("</todo>")
			return sb.String()
		}
	}

	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var attrs, body strings.Builder
	for _, k := range keys {
		v := args[k]
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && (strings.Contains(s, "\n") || len(s) > 120) {
			fmt.Fprintf(&body, "<parameter name=\"%s\">\n%s\n</parameter>\n", escapeXMLAttr(k), s)
			continue
		}
		if _, isMap := v.(map[string]any); isMap {
			if b, err := json.Marshal(v); err == nil {
				fmt.Fprintf(&body, "<parameter name=\"%s\">%s</parameter>\n", escapeXMLAttr(k), string(b))
				continue
			}
		}
		if _, isSlice := v.([]any); isSlice {
			if b, err := json.Marshal(v); err == nil {
				fmt.Fprintf(&body, "<parameter name=\"%s\">%s</parameter>\n", escapeXMLAttr(k), string(b))
				continue
			}
		}
		attrs.WriteString(xmlAttrString(k, v))
	}

	useDirectTag := !strings.Contains(name, ":") && !strings.Contains(name, " ") && name != ""
	if useDirectTag {
		if body.Len() == 0 {
			return fmt.Sprintf("<%s%s/>", name, attrs.String())
		}
		return fmt.Sprintf("<%s%s>\n%s</%s>", name, attrs.String(), body.String(), name)
	}
	if body.Len() == 0 {
		return fmt.Sprintf("<tool_call name=\"%s\"%s/>", escapeXMLAttr(name), attrs.String())
	}
	return fmt.Sprintf("<tool_call name=\"%s\"%s>\n%s</tool_call>", escapeXMLAttr(name), attrs.String(), body.String())
}
