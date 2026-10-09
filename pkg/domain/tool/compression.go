package tool

import (
	"strings"
)

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

func setPropDesc(props map[string]SchemaProp, key, desc string) {
	if props == nil {
		return
	}
	if prop, ok := props[key]; ok {
		prop.Description = desc
		props[key] = prop
	}
}

// CompressToolDefinition generates a concise schema definition for models with smaller context budgets.
func CompressToolDefinition(t Tool) Tool {
	compressed := t
	newProps := make(map[string]SchemaProp)
	for k, v := range t.Function.Parameters.Properties {
		newProps[k] = v
	}
	compressed.Function.Parameters.Properties = newProps
	props := compressed.Function.Parameters.Properties

	switch t.Function.Name {
	case "bash":
		compressed.Function.Description = "Run shell commands (builds, tests, git). Use read instead of cat/head/tail to inspect files."
		setPropDesc(props, "command", "Command string")
		setPropDesc(props, "background", "Run in background")
	case "list", "ls":
		compressed.Function.Description = "List directory contents"
		setPropDesc(props, "path", "Directory path")
		setPropDesc(props, "depth", "Depth limit")
	case "find":
		compressed.Function.Description = "Find files matching a glob pattern"
		setPropDesc(props, "pattern", "Glob pattern")
		setPropDesc(props, "path", "Search directory")
	case "read":
		compressed.Function.Description = "Read file contents. Path must be a specific file, not a directory. Specify 'path' first. Use 'list' to inspect directory trees."
		setPropDesc(props, "path", "File path (specify first)")
		setPropDesc(props, "offset", "Start line")
		setPropDesc(props, "limit", "Max lines")
	case "grep":
		compressed.Function.Description = "Search for functions, symbols, or regex across files. Use this first to locate elements before editing."
		setPropDesc(props, "pattern", "Search regex or string")
		setPropDesc(props, "path", "Dir or file to search")
		setPropDesc(props, "glob", "File glob filter (e.g. *.go)")
		setPropDesc(props, "ignore_case", "Case-insensitive")
		setPropDesc(props, "literal", "Literal string search")
		setPropDesc(props, "context", "Surrounding context lines")
		setPropDesc(props, "limit", "Max matches")
	case "write":
		compressed.Function.Description = "Create or overwrite a file. Always specify 'path' first before 'content'. Never use after an edit mismatch."
		setPropDesc(props, "path", "File path (specify first)")
		setPropDesc(props, "content", "File content")
		setPropDesc(props, "write_content", "File content")
	case "edit":
		compressed.Function.Description = "Replace exact unique blocks copied from the latest read. Target only the necessary element using a smaller block. Never overwrite whole files."
		setPropDesc(props, "path", "File path")
		if prop, ok := props["updates"]; ok {
			prop.Description = "Exact replacements from current file content"
			if prop.Items != nil {
				itemsCopy := *prop.Items
				itemsProps := make(map[string]SchemaProp)
				for k, v := range itemsCopy.Properties {
					itemsProps[k] = v
				}
				setPropDesc(itemsProps, "oldText", "Exact unique current text copied from latest read")
				setPropDesc(itemsProps, "newText", "Complete replacement for oldText")
				itemsCopy.Properties = itemsProps
				prop.Items = &itemsCopy
			}
			props["updates"] = prop
		}
	case "load_skill":
		compressed.Function.Description = "Load skill instructions"
		setPropDesc(props, "name", "Skill name")
	case "task_status":
		compressed.Function.Description = "Check task status"
		setPropDesc(props, "task_id", "Task ID")
	case "task_kill":
		compressed.Function.Description = "Kill task"
		setPropDesc(props, "task_id", "Task ID")
	case "create_subagent", "spawn_subagent":
		compressed.Function.Description = "Create specialized subagent"
		setPropDesc(props, "name", "Subagent name")
		setPropDesc(props, "system_prompt", "Role and instructions")
	case "remove_subagent":
		compressed.Function.Description = "Terminate subagent"
		setPropDesc(props, "name", "Subagent name")
	case "list_subagents", "swarm_topology":
		compressed.Function.Description = "View active subagents"
	case "audit_subagent", "swarm_audit":
		compressed.Function.Description = "Audit subagent execution"
		setPropDesc(props, "name", "Subagent name")
	default:
		if strings.HasPrefix(t.Function.Name, "subagent__") {
			compressed.Function.Description = "Delegate task to subagent"
			setPropDesc(props, "prompt", "Task prompt")
		} else {
			compressed.Function.Description = truncateRunes(compressed.Function.Description, 50)
			for k, prop := range props {
				prop.Description = truncateRunes(prop.Description, 40)
				props[k] = prop
			}
		}
	}
	return compressed
}

// PrepareToolDefinitions formats and optionally compresses schemas for context minimization.
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
