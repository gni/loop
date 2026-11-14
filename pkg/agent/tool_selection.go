package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/domain/task"
)

func (a *Agent) selectInferenceTools(ctx context.Context, tools []tool.Tool, allowlist []string) []tool.Tool {
	cfg := a.configForRun(ctx)
	if cfg == nil || allowlist != nil || strings.EqualFold(cfg.ToolProtocol, "xml") {
		return tools
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ToolExposure))
	if mode == "full" || ((mode == "" || mode == "auto") && a.GetEffectiveContextLimit(0) > 32768) {
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
	selected := make([]tool.Tool, 0, len(tools))
	for _, definition := range tools {
		name := definition.Function.Name
		needed := a.Registry.IsExposed(name)
		if current != nil {
			needed = run.ToolDiscovered(ctx, name)
		}
		if strings.HasPrefix(name, "mcp__") {
			if owner, ok := a.MCPToolServer(name); ok && cfg.MCPServers[owner].Exposure != "search" {
				needed = true
			}
		}
		switch name {
		case "read", "edit", "write", "bash", "grep", "find_files", "list_directory", "search_tools", "ask_user", "load_skill":
			needed = true
		case "todo":
			needed = needed || cfg.GuidedMode || (current != nil && len(current.Plan) > 0)
		case "task_state":
			needed = needed || task.NeedsProgress(current)
		case "get_task_status", "stop_task":
			needed = needed || a.CountActiveTasks() > 0
		case "search_symbols":
			if current != nil {
				text := strings.ToLower(current.Objective)
				for _, word := range []string{"symbol", "definition", "caller", "reference"} {
					needed = needed || strings.Contains(text, word)
				}
			}
		}
		if needed {
			selected = append(selected, definition)
		}
	}
	return selected
}
