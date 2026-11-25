package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/domain/task"
)

func (a *Agent) hasSpawnedAgents() bool {
	if a == nil {
		return false
	}
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return len(a.SpawnedAgents) > 0
}

func (a *Agent) selectInferenceTools(ctx context.Context, tools []tool.Tool, allowlist []string) []tool.Tool {
	cfg := a.configForRun(ctx)
	if cfg == nil || allowlist != nil || strings.EqualFold(cfg.ToolProtocol, "xml") {
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
