package agent

import (
	"context"
	"fmt"
	"strings"

	"loop/pkg/agent/fallback"
	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/task"
	domaintool "loop/pkg/domain/tool"
)

// jsonToolProtocol implements native JSON function-calling schemas, adaptive
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

	current := run.TaskSnapshot(ctx)
	objective := ""
	if current != nil {
		objective = strings.ToLower(current.Objective)
	}
	hasSkills := len(a.GetActiveSkills()) > 0
	hasSpawned := a.hasSpawnedAgents()
	askUserEnabled := !strings.EqualFold(strings.TrimSpace(cfg.AskUserMode), "disabled")

	selected := make([]tool.Tool, 0, len(tools))
	for _, definition := range tools {
		name := definition.Function.Name
		needed := a.Registry.IsExposed(name)
		if current != nil {
			needed = needed || run.ToolDiscovered(ctx, name)
		}
		if strings.HasPrefix(name, "mcp__") {
			if owner, ok := a.MCPToolServer(name); ok && cfg.MCPServers[owner].Exposure != "search" {
				needed = true
			}
		}
		if strings.HasPrefix(name, "subagent__") {
			needed = true
		}
		switch name {
		case "read", "edit", "write", "bash", "grep", "find_files", "list_directory", "todo", "search_tools":
			needed = true
		case "load_skill":
			needed = needed || hasSkills
		case "ask_user":
			needed = needed || askUserEnabled
		case "task_state":
			needed = needed || task.NeedsProgress(current)
		case "get_task_status", "stop_task":
			needed = needed || a.CountActiveTasks() > 0
		case "remove_subagent", "list_subagents", "inspect_subagent":
			needed = needed || hasSpawned
		case "create_subagent":
			if hasSpawned {
				needed = true
			} else if objective != "" {
				for _, word := range []string{"subagent", "delegate", "swarm"} {
					needed = needed || strings.Contains(objective, word)
				}
			}
		case "search_symbols":
			if objective != "" {
				for _, word := range []string{"symbol", "definition", "caller", "reference"} {
					needed = needed || strings.Contains(objective, word)
				}
			}
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
