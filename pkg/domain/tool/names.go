package tool

import (
	"sort"
	"strings"
)

var builtinNames = map[string]bool{
	"bash": true, "read": true, "write": true, "edit": true, "grep": true,
	"find_files": true, "list_directory": true, "search_symbols": true,
	"get_task_status": true, "stop_task": true, "todo": true, "task_state": true,
	"ask_user": true, "load_skill": true, "load_mcp": true, "search_tools": true,
	"create_subagent": true, "remove_subagent": true, "list_subagents": true, "inspect_subagent": true,
	"list_mcp_resources": true, "list_mcp_resource_templates": true, "read_mcp_resource": true,
}

// BuiltinToolNames is the shared catalog for classification and fallback parsing.
func BuiltinToolNames() []string {
	names := make([]string, 0, len(builtinNames))
	for name := range builtinNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func IsBuiltinName(name string) bool { return builtinNames[NormalizeName(name)] }

// LegacyToolNames remain valid in old sessions, settings, and model output.
func LegacyToolNames() map[string]string {
	return map[string]string{
		"list": "list_directory", "find": "find_files", "task_status": "get_task_status", "task_kill": "stop_task", "audit_subagent": "inspect_subagent",
	}
}

// NormalizeName preserves integration names and normalizes builtin names/aliases.
func NormalizeName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	if builtinNames[lower] {
		return lower
	}
	switch strings.ToLower(trimmed) {
	case "write_path", "write_file", "writefile", "create_file", "createfile", "write_to_file", "writetofile", "save_file", "savefile", "new_file":
		return "write"
	case "edit_file", "editfile", "modify_file", "patch_file", "replace_file_content", "str_replace", "replace", "patch":
		return "edit"
	case "read_file", "readfile", "cat", "view_file", "view", "cat_file", "open_file":
		return "read"
	case "list", "list_dir", "list_directory", "listdir", "ls", "dir", "list_files", "listfiles", "directory_list":
		return "list_directory"
	case "find", "find_files", "find_file", "findfiles", "findfile", "locate_files", "locate_file", "glob", "file_search":
		return "find_files"
	case "grep_search", "search_code", "search", "grep_files", "search_files", "ripgrep", "rg":
		return "grep"
	case "list_subagents", "subagents_list", "subagent_list", "list_agents", "swarm_topology":
		return "list_subagents"
	case "inspect_subagent", "audit_subagent", "subagent_audit", "subagent_history", "audit_subagents", "swarm_audit":
		return "inspect_subagent"
	case "kill_subagent", "terminate_subagent", "delete_subagent":
		return "remove_subagent"
	case "create_subagent", "new_subagent", "spawn_subagent", "spawn", "add_subagent":
		return "create_subagent"
	case "task_kill", "kill_task", "stop_task", "task_stop", "taskstop", "taskkill", "terminate_task", "killtask", "stoptask", "task_terminate":
		return "stop_task"
	case "task_status", "status_task", "get_task_status", "tasks", "task_list", "list_tasks", "taskstatus", "task_info", "taskinfo", "task_view", "taskview", "task_output", "taskoutput", "check_task", "get_task":
		return "get_task_status"
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
