package tool

import (
	"sort"
	"strings"
	"unicode"

	"loop/pkg/domain/limits"
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

// NormalizeName preserves integration names and normalizes builtin names/aliases
// as well as subagent delegation tool names.
func NormalizeName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	if builtinNames[lower] {
		return lower
	}
	if strings.HasPrefix(lower, "subagent__") {
		if normalized := NormalizeAgentName(trimmed); normalized != "" {
			return "subagent__" + normalized
		}
		return trimmed
	}
	switch lower {
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

func foldLatinRune(r rune) (string, bool) {
	switch r {
	case 'à', 'á', 'â', 'ã', 'ä', 'å', 'À', 'Á', 'Â', 'Ã', 'Ä', 'Å':
		return "a", true
	case 'æ', 'Æ':
		return "ae", true
	case 'ç', 'Ç':
		return "c", true
	case 'è', 'é', 'ê', 'ë', 'È', 'É', 'Ê', 'Ë':
		return "e", true
	case 'ì', 'í', 'î', 'ï', 'Ì', 'Í', 'Î', 'Ï':
		return "i", true
	case 'ñ', 'Ñ':
		return "n", true
	case 'ò', 'ó', 'ô', 'õ', 'ö', 'ø', 'Ò', 'Ó', 'Ô', 'Õ', 'Ö', 'Ø':
		return "o", true
	case 'œ', 'Œ':
		return "oe", true
	case 'ß':
		return "ss", true
	case 'ù', 'ú', 'û', 'ü', 'Ù', 'Ú', 'Û', 'Ü':
		return "u", true
	case 'ý', 'ÿ', 'Ý', 'Ÿ':
		return "y", true
	default:
		return "", false
	}
}

// classifySlugRune returns the lowercase ASCII emission and class for r:
// 0 = separator/ignored, 1 = lowercase letter, 2 = uppercase letter, 3 = digit.
func classifySlugRune(r rune) (string, int) {
	switch {
	case r >= 'a' && r <= 'z':
		return string(r), 1
	case r >= 'A' && r <= 'Z':
		return string(r + ('a' - 'A')), 2
	case r >= '0' && r <= '9':
		return string(r), 3
	}
	if folded, ok := foldLatinRune(r); ok {
		if unicode.IsUpper(r) {
			return folded, 2
		}
		return folded, 1
	}
	return "", 0
}

// NormalizeSlug converts free-form text (such as "James Bond" or "CodeReviewer!")
// into a safe lowercase snake_case ASCII identifier capped to maxLen bytes.
func NormalizeSlug(raw string, maxLen int) string {
	runes := []rune(strings.TrimSpace(raw))
	if len(runes) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(runes))
	prevSep := true
	prevKind := 0
	for i, r := range runes {
		out, kind := classifySlugRune(r)
		if kind == 0 {
			prevSep = true
			prevKind = 0
			continue
		}
		if b.Len() > 0 {
			needUnderscore := prevSep
			if !needUnderscore && kind == 2 {
				if prevKind == 1 {
					needUnderscore = true
				} else if prevKind == 2 && i+1 < len(runes) {
					_, nextKind := classifySlugRune(runes[i+1])
					if nextKind == 1 {
						needUnderscore = true
					}
				}
			}
			if needUnderscore {
				b.WriteByte('_')
			}
		}
		b.WriteString(out)
		prevSep = false
		prevKind = kind
	}
	slug := strings.Trim(b.String(), "_")
	if maxLen > 0 && len(slug) > maxLen {
		slug = strings.TrimRight(slug[:maxLen], "_")
	}
	return slug
}

// NormalizeSkillName converts a free-form inline skill name into a safe
// lowercase snake_case identifier bounded by the configured name limit.
func NormalizeSkillName(raw string) string {
	maxLen := limits.NameMaxLength()
	if maxLen <= 0 {
		maxLen = limits.DefaultNameMaxLength
	}
	return NormalizeSlug(raw, maxLen)
}

// NormalizeAgentName converts free-form agent names (such as "James Bond") into
// safe lowercase snake_case identifiers accepted by storage, tools, and XML tags.
func NormalizeAgentName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	for strings.HasPrefix(strings.ToLower(trimmed), "subagent__") {
		trimmed = strings.TrimSpace(trimmed[len("subagent__"):])
	}
	maxLen := limits.NameMaxLength()
	if maxLen <= 0 {
		maxLen = limits.DefaultNameMaxLength
	}
	slug := NormalizeSlug(trimmed, maxLen)
	if slug == "" {
		return ""
	}
	if strings.HasSuffix(slug, "_state") {
		const suffix = "_agent"
		if len(slug)+len(suffix) > maxLen {
			cut := maxLen - len(suffix)
			if cut > 0 {
				slug = strings.TrimRight(slug[:cut], "_")
				for strings.HasSuffix(slug, "_state") {
					slug = strings.TrimRight(strings.TrimSuffix(slug, "_state"), "_")
				}
			}
		}
		if slug == "" {
			slug = "agent"
		} else if len(slug)+len(suffix) <= maxLen {
			slug += suffix
		} else {
			slug = strings.TrimRight(strings.TrimSuffix(slug, "_state"), "_")
			if slug == "" {
				slug = "agent"
			}
		}
	}
	return slug
}
