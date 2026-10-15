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
	case "write_path", "write_file", "writefile", "create_file", "createfile", "write_to_file", "writetofile", "save_file", "savefile", "new_file":
		return "write"
	case "edit_file", "editfile", "modify_file", "patch_file", "replace_file_content", "str_replace", "replace", "patch":
		return "edit"
	case "read_file", "readfile", "cat", "view_file", "view", "cat_file", "open_file":
		return "read"
	case "list_dir", "list_directory", "listdir", "ls", "dir", "list_files", "listfiles", "directory_list":
		return "list"
	case "find_files", "find_file", "findfiles", "findfile", "locate_files", "locate_file", "glob", "file_search":
		return "find"
	case "grep_search", "search_code", "search", "grep_files", "search_files", "ripgrep", "rg":
		return "grep"
	case "list_subagents", "subagents_list", "subagent_list", "list_agents", "swarm_topology":
		return "list_subagents"
	case "audit_subagent", "subagent_audit", "subagent_history", "audit_subagents", "swarm_audit":
		return "audit_subagent"
	case "kill_subagent", "terminate_subagent", "delete_subagent":
		return "remove_subagent"
	case "create_subagent", "new_subagent", "spawn_subagent", "spawn", "add_subagent":
		return "create_subagent"
	case "kill_task", "stop_task", "task_stop", "taskstop", "taskkill", "terminate_task", "killtask", "stoptask", "task_terminate":
		return "task_kill"
	case "status_task", "get_task_status", "tasks", "task_list", "list_tasks", "taskstatus", "task_info", "taskinfo", "task_view", "taskview", "task_output", "taskoutput", "check_task", "get_task":
		return "task_status"
	case "run_command", "runcommand", "exec", "shell", "terminal", "execute_command", "execute_bash", "cmd", "sh", "command", "bash_command":
		return "bash"
	case "todo_write", "todowrite", "todo_list", "todolist", "plan", "plan_tasks", "update_todo", "todos":
		return "todo"
	case "ask_user_question", "askuser", "ask_human", "question_user", "ask", "ask_question", "confirm":
		return "ask_user"
	case "skill", "loadskill", "use_skill", "read_skill":
		return "load_skill"
	case "loadmcp", "use_mcp", "mcp_load", "connect_mcp":
		return "load_mcp"
	default:
		return trimmed
	}
}
