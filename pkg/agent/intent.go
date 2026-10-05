package agent

import (
	"strings"

	"loop/pkg/agent/tool"
)

// Single source of truth for tool side-effect classification.
//
// inspection: no writes, no execution, no process spawn
// action:     writes files, executes commands, spawns or kills processes
var inspectionTools = map[string]bool{
	"read": true, "grep": true, "find": true, "list": true, "ls": true,
	"load_skill": true, "task_status": true,
	"list_subagents": true, "audit_subagent": true,
	"swarm_topology": true, "swarm_audit": true,
}

var actionTools = map[string]bool{
	"write": true, "edit": true, "bash": true, "task_kill": true,
	"create_subagent": true, "spawn_subagent": true, "remove_subagent": true,
}

// IsInspectionTool reports whether a tool is side-effect free.
func IsInspectionTool(name string) bool { return inspectionTools[tool.NormalizeName(name)] }

// IsActionTool reports whether a tool can mutate state.
func IsActionTool(name string) bool {
	norm := tool.NormalizeName(name)
	return actionTools[norm] || strings.HasPrefix(norm, "subagent__")
}

// NeedsApproval is the approval decision used by the loop: inspection never
// requires approval, everything else does.
func NeedsApproval(name string) bool { return !IsInspectionTool(name) }
