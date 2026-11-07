package tool

// Descriptions explain behavior; field names, types, enums and required lists
// already describe the arguments. Keep only parameter hints that add meaning.
var compactToolDescriptions = map[string]string{
	"bash":             "Run shell commands. Working directory persists; background starts a task.",
	"list_directory":   "List directory entries.",
	"find_files":       "Find paths by glob.",
	"read":             "Read a file; offset/limit paginate. Use list_directory for directories.",
	"grep":             "Search file content by regex (literal=true for fixed text). glob filters files.",
	"write":            "Create or fully replace a file; never bypass an edit mismatch.",
	"edit":             "Replace unique oldText with newText. On mismatch re-read and retry a smaller block; never bypass with write.",
	"todo":             "Manage the todo plan, separate from background tasks; at most one in_progress. Prefer markdown for full plans; actions update individuals.",
	"ask_user":         "Ask for needed clarification or approval with clear choices. The UI adds Other.",
	"load_skill":       "Load a reference guide by name.",
	"load_mcp":         "Connect a deferred MCP server by name.",
	"get_task_status":  "Inspect background task status and output.",
	"stop_task":        "Stop a background task.",
	"create_subagent":  "Create an autonomous agent for a bounded task.",
	"remove_subagent":  "Stop and remove an agent.",
	"list_subagents":   "List active agents.",
	"inspect_subagent": "Inspect an agent's execution history.",
}

var compactParameterHints = map[string]map[string]string{
	"bash":           {"dir": "Override working directory."},
	"read":           {"offset": "1-based line.", "limit": "Lines to read."},
	"list_directory": {"offset": "1-based entry.", "depth": "Recursion depth."},
	"find_files":     {"offset": "1-based result."},
	"grep":           {"offset": "1-based match."},
	"todo": {
		"markdown": "Full plan: - [ ] task-1: Work; [>] active, [x] done. One row per task; stable IDs. Do not combine with tasks or action.",
		"tasks":    "Full plan replacement.",
		"action":   "update/remove need task_id; add needs task.",
	},
	"ask_user":        {"recommended": "Option label."},
	"create_subagent": {"system_prompt": "Task scope and operating instructions.", "inline_skills": "Private reference instructions."},
}

// CompactToolGuidelines omits only builtin rules already in native schemas.
// Explicit catalog and executor guidelines remain verbatim.
func CompactToolGuidelines(name string, guidelines []string) []string {
	name = NormalizeName(name)
	if hasToolPromptOverride(name) {
		return guidelines
	}
	switch name {
	case "read", "write", "edit", "ask_user", "bash", "list_directory", "find_files", "grep", "todo", "get_task_status", "stop_task":
		return nil
	}
	return guidelines
}

func compactSchemaProp(prop SchemaProp) SchemaProp {
	prop.Description = ""
	prop.Properties = compactSchemaProperties(prop.Properties)
	if prop.Items != nil {
		item := compactSchemaProp(*prop.Items)
		prop.Items = &item
	}
	return prop
}

func compactSchemaProperties(props map[string]SchemaProp) map[string]SchemaProp {
	if props == nil {
		return nil
	}
	copied := make(map[string]SchemaProp, len(props))
	for key, prop := range props {
		copied[key] = compactSchemaProp(prop)
	}
	return copied
}

func CompressToolDefinition(t Tool) Tool {
	if hasToolPromptOverride(t.Function.Name) {
		return t
	}
	compressed := t
	if description, known := compactToolDescriptions[NormalizeName(t.Function.Name)]; known {
		compressed.Function.Description = description
		props := compactSchemaProperties(t.Function.Parameters.Properties)
		for key, hint := range compactParameterHints[NormalizeName(t.Function.Name)] {
			if prop, ok := props[key]; ok {
				prop.Description = hint
				props[key] = prop
			}
		}
		compressed.Function.Parameters.Properties = props
		return compressed
	}
	// Unfamiliar integration schemas may carry essential instructions.
	return t
}

func PrepareToolDefinitions(tools []Tool, compact bool) []Tool {
	if !compact {
		return tools
	}
	prepared := make([]Tool, len(tools))
	for i, t := range tools {
		prepared[i] = CompressToolDefinition(t)
	}
	return prepared
}
