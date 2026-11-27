package agent

import (
	"context"
	"fmt"
	"strings"

	"loop/pkg/agent/fallback"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	domaintool "loop/pkg/domain/tool"
)

// jsonToolProtocol implements native JSON function-calling schemas, session-stable
// tool exposure, native tool_calls/tool history turns, and passthrough text streaming.
type jsonToolProtocol struct{}

func (jsonToolProtocol) Name() string                  { return "json" }
func (jsonToolProtocol) UsesNativeSchemas() bool       { return true }
func (jsonToolProtocol) CompactToolSearchResult() bool { return true }

func (jsonToolProtocol) SelectInferenceTools(a *Agent, ctx context.Context, tools []tool.Tool, allowlist []string) []tool.Tool {
	cfg := a.configForRun(ctx)
	if cfg == nil || allowlist != nil {
		return tools
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ToolExposure))
	if mode == "full" {
		return tools
	}
	discovery := false
	for _, definition := range tools {
		if definition.Function.Name == "search_tools" {
			discovery = true
			break
		}
	}
	if !discovery {
		return tools
	} // Never hide capabilities without a discovery route.

	// Keep the selected tool catalog strictly deterministic across turns and
	// steps so the serialized tool prefix stays KV-cache-aligned. Volatile
	// per-turn state (prompt keywords, active background tasks, task progress)
	// must never add or remove schemas mid-session.
	askUserEnabled := !strings.EqualFold(strings.TrimSpace(cfg.AskUserMode), "disabled")
	subagentsEnabled := cfg.MaxSubagentDepth > 0

	selected := make([]tool.Tool, 0, len(tools))
	for _, definition := range tools {
		name := definition.Function.Name
		needed := a.Registry.IsExposed(name)
		if strings.HasPrefix(name, "mcp__") {
			if owner, ok := a.MCPToolServer(name); ok && cfg.MCPServers[owner].Exposure != "search" {
				needed = true
			}
		}
		if strings.HasPrefix(name, "subagent__") {
			needed = true
		}
		switch name {
		case "read", "edit", "write", "bash", "grep", "find_files", "list_directory", "todo", "search_tools",
			"task_state", "get_task_status", "stop_task", "search_symbols", "load_skill":
			needed = true
		case "ask_user":
			needed = needed || askUserEnabled
		case "create_subagent", "remove_subagent", "list_subagents", "inspect_subagent":
			needed = needed || subagentsEnabled
		}
		if needed {
			selected = append(selected, definition)
		}
	}
	return selected
}

func (jsonToolProtocol) PrepareWireTools(tools []tool.Tool, compact bool) []tool.Tool {
	return prepareToolDefinitions(tools, compact)
}

func (jsonToolProtocol) PrepareWireMessages(messages []db.Message) []db.Message {
	return messages
}

func (jsonToolProtocol) FormatPromptTools(sb *strings.Builder, _ []ToolEntry, toolNames map[string]bool, cfg SystemPromptConfig) {
	// Native JSON tool schemas already carry names, descriptions, and parameters.
	// Only list active subagents that are not yet attached as native tool definitions.
	t := tpl()
	agentTpl := domaintool.AgentTemplatesSnapshot()
	var toolLines strings.Builder
	for _, name := range cfg.ActiveAgents {
		toolName := "subagent__" + name
		if !tool.IsAllowedTool(toolName, cfg.Allowlist) || toolNames[toolName] {
			continue
		}
		toolLines.WriteString(fmt.Sprintf(t.SubagentLine, name, fmt.Sprintf(agentTpl.SubagentPrompt, name)))
	}
	if toolLines.Len() > 0 {
		sb.WriteString(fmt.Sprintf(t.ToolsSection, toolLines.String()))
	}
}

func (jsonToolProtocol) NewStreamTextFilter(emit func(string), _ func(string, int), _ func(string, int), _ func(string, int)) *FallbackToolTextFilter {
	return fallback.NewPlainTextFilter(emit)
}

func (jsonToolProtocol) ParseAssistantToolCalls(rawText string, nativeCalls []db.ToolCall) ([]db.ToolCall, string, error) {
	return nativeCalls, rawText, nil
}

func (jsonToolProtocol) CleanReasoning(raw string) string {
	return raw
}
