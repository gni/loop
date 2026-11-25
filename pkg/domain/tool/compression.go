package tool

// Descriptions explain behavior; field names, types, enums and required lists
// already describe the arguments. Validation, pagination notices, and error
// recovery hints are enforced in Go tool executors instead of repeated prose.
var compactToolDescriptions = map[string]string{
	"bash":                        "Run shell command (cwd persists; background=true tracks task).",
	"list_directory":              "List directory entries.",
	"find_files":                  "Find files by glob.",
	"read":                        "Read file (text or image).",
	"grep":                        "Search file contents (regex; literal=true for fixed text).",
	"search_symbols":              "Find Go/JS/TS/Python declarations and call sites.",
	"write":                       "Create or overwrite file.",
	"edit":                        "Replace unique oldText with newText in file.",
	"todo":                        "Manage task plan. Use markdown ('- [ ] id: task', [>] active, [x] done) or action.",
	"ask_user":                    "Ask user for blocking clarification or approval. When listing options, set recommended to the exact label of the best option (UI tags it and adds Other).",
	"load_skill":                  "Load skill by name, or search metadata by query.",
	"load_mcp":                    "Connect deferred MCP server (omit name to list).",
	"search_tools":                "Discover and enable specialized tools for next step.",
	"task_state":                  "Update multi-step progress or blockers.",
	"list_mcp_resources":          "List MCP resources.",
	"list_mcp_resource_templates": "List MCP resource templates.",
	"read_mcp_resource":           "Read MCP resource by server and URI.",
	"get_task_status":             "Check background task status and output.",
	"stop_task":                   "Stop background task.",
	"create_subagent":             "Spawn subagent for a bounded task.",
	"remove_subagent":             "Stop and remove subagent.",
	"list_subagents":              "List active subagents.",
	"inspect_subagent":            "Inspect subagent execution history.",
}

var compactParameterHints = map[string]map[string]string{
	"ask_user": {
		"recommended": "Exact label of the recommended option from options (UI tags it as Recommended).",
	},
	"todo": {
		"markdown": "Checklist lines: '- [ ] id: task' ([>] active, [x] done). Omit action when sending markdown.",
		"action":   "Single-item op: update, add, remove, or list.",
		"task_id":  "Task ID for update or remove (e.g. 'task-1').",
	},
	"task_state": {
		"remaining": "Replace remaining items ([] clears).",
	},
}

// CompactToolGuidelines omits builtin rules already enforced in Go or native schemas.
// Explicit catalog and executor guidelines remain verbatim.
func CompactToolGuidelines(name string, guidelines []string) []string {
	name = NormalizeName(name)
	if hasToolPromptOverride(name) {
		return guidelines
	}
	switch name {
	case "read", "write", "edit", "ask_user", "bash", "list_directory", "find_files", "grep", "todo", "get_task_status", "stop_task", "task_state", "search_symbols", "create_subagent", "load_skill", "search_tools":
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
	canonical := NormalizeName(t.Function.Name)
	compressed := t
	if description, known := compactToolDescriptions[canonical]; known {
		compressed.Function.Description = description
		props := compactSchemaProperties(t.Function.Parameters.Properties)
		if canonical == "todo" {
			// Keep markdown + action in the wire schema; Go still accepts tasks arrays if sent.
			delete(props, "tasks")
		}
		if canonical == "create_subagent" {
			if prop, ok := props["skill_names"]; ok && prop.Items != nil {
				item := *prop.Items
				item.Enum = nil
				prop.Items = &item
				props["skill_names"] = prop
			}
		}
		for key, hint := range compactParameterHints[canonical] {
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
