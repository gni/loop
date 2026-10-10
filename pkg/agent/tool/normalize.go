package tool

import "strings"

// Canonical subagent tool names (post-normalization). Single source of truth for the
// spawn gate and the inspection/action classification; previously duplicated in
// pkg/agent/intent.go and pkg/agent/swarm/agent.go.
var subagentTools = map[string]bool{
	"create_subagent": true,
	"remove_subagent": true,
	"list_subagents":  true,
	"audit_subagent":  true,
}

var subagentSpawnTools = map[string]bool{
	"create_subagent": true,
}

var subagentInspectionTools = map[string]bool{
	"list_subagents":  true,
	"audit_subagent":  true,
}

// IsSubagentTool reports whether a tool name belongs to the subagent family, including
// the per-agent delegation aliases (subagent__<name>).
func IsSubagentTool(name string) bool {
	norm := NormalizeName(name)
	return subagentTools[norm] || strings.HasPrefix(norm, "subagent__")
}

// IsSubagentInspectionTool reports the side-effect-free members of the subagent family.
func IsSubagentInspectionTool(name string) bool {
	return subagentInspectionTools[NormalizeName(name)]
}

// IsSpawnTool reports whether a tool can spawn a subagent.
func IsSpawnTool(name string) bool {
	return subagentSpawnTools[NormalizeName(name)]
}

// NormalizeName maps common hallucinated tool names and aliases from local/quantized
// models to canonical tool names registered in the harness.
func NormalizeName(name string) string {
	trimmed := strings.TrimSpace(name)
	switch strings.ToLower(trimmed) {
	case "write_path", "write_file", "writefile", "create_file", "createfile", "write_to_file", "writetofile":
		return "write"
	case "edit_file", "editfile", "modify_file", "patch_file":
		return "edit"
	case "read_file", "readfile", "cat":
		return "read"
	case "list_dir", "list_directory", "listdir", "ls", "dir":
		return "list"
	case "find_files", "find_file", "findfiles", "findfile":
		return "find"
	case "grep_search", "search_code", "search":
		return "grep"
	case "list_subagents", "subagents_list", "subagent_list", "list_agents", "swarm_topology":
		return "list_subagents"
	case "audit_subagent", "subagent_audit", "subagent_history", "audit_subagents", "swarm_audit":
		return "audit_subagent"
	case "kill_subagent", "terminate_subagent", "delete_subagent":
		return "remove_subagent"
	case "create_subagent", "new_subagent", "spawn_subagent", "spawn", "add_subagent":
		return "create_subagent"
	case "kill_task", "stop_task":
		return "task_kill"
	case "status_task", "get_task_status", "tasks", "task_list", "list_tasks":
		return "task_status"
	case "run_command", "runcommand", "exec", "shell", "terminal":
		return "bash"
	case "todo_write", "todowrite", "todo_list", "todolist", "plan", "plan_tasks":
		return "todo"
	case "ask_user_question", "askuser", "ask_human", "question_user", "ask":
		return "ask_user"
	default:
		return trimmed
	}
}
