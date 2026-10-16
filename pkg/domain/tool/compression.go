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

func CompressToolDefinition(t Tool) Tool {
	compressed := t
	newProps := make(map[string]SchemaProp, len(t.Function.Parameters.Properties))
	for k, v := range t.Function.Parameters.Properties {
		newProps[k] = v
	}
	compressed.Function.Parameters.Properties = newProps
	props := compressed.Function.Parameters.Properties

	switch t.Function.Name {
	case "bash":
		compressed.Function.Description = "Run shell commands (builds, tests, git). Use read/grep/find/list for file inspection."
		setPropDesc(props, "command", "Shell command")
		setPropDesc(props, "dir", "Working directory")
		setPropDesc(props, "background", "Run in background")
	case "list", "ls":
		compressed.Function.Description = "List directory contents"
		setPropDesc(props, "path", "Directory path")
		setPropDesc(props, "depth", "Depth limit")
		setPropDesc(props, "offset", "Start entry")
	case "find":
		compressed.Function.Description = "Find files matching a glob pattern"
		setPropDesc(props, "pattern", "Glob pattern")
		setPropDesc(props, "path", "Search directory")
		setPropDesc(props, "limit", "Max results")
		setPropDesc(props, "offset", "Start result")
	case "read":
		compressed.Function.Description = "Read file contents. Specify path first. Use list for directories."
		setPropDesc(props, "path", "File path (specify first)")
		setPropDesc(props, "offset", "Start line (1-indexed)")
		setPropDesc(props, "limit", "Max lines")
	case "grep":
		compressed.Function.Description = "Search file contents for regex or literal strings across workspace."
		setPropDesc(props, "pattern", "Regex or literal string")
		setPropDesc(props, "path", "Dir or file")
		setPropDesc(props, "glob", "File glob filter (e.g. *.go)")
		setPropDesc(props, "ignore_case", "Case-insensitive")
		setPropDesc(props, "literal", "Literal string match")
		setPropDesc(props, "context", "Context lines")
		setPropDesc(props, "limit", "Max matches")
		setPropDesc(props, "offset", "Start match")
	case "write":
		compressed.Function.Description = "Create or overwrite a file. Specify path first before content."
		setPropDesc(props, "path", "File path (specify first)")
		setPropDesc(props, "content", "Complete file content")
	case "edit":
		compressed.Function.Description = "Replace exact unique blocks copied from latest read."
		setPropDesc(props, "path", "File path")
		if prop, ok := props["updates"]; ok {
			prop.Description = "Targeted text replacements"
			if prop.Items != nil {
				itemsCopy := *prop.Items
				itemsProps := make(map[string]SchemaProp, len(itemsCopy.Properties))
				for k, v := range itemsCopy.Properties {
					itemsProps[k] = v
				}
				setPropDesc(itemsProps, "oldText", "Exact unique lines from file")
				setPropDesc(itemsProps, "newText", "Replacement text")
				itemsCopy.Properties = itemsProps
				prop.Items = &itemsCopy
			}
			props["updates"] = prop
		}
	case "todo":
		compressed.Function.Description = "Track multi-step plan. Exactly one task in_progress at a time."
		setPropDesc(props, "tasks", "Full task list")
		setPropDesc(props, "action", "update, add, remove, or list")
		setPropDesc(props, "task_id", "Task ID")
		setPropDesc(props, "status", "pending, in_progress, or completed")
		setPropDesc(props, "task", "Task description")
	case "ask_user":
		compressed.Function.Description = "Ask user a structured question with selectable options."
		setPropDesc(props, "question", "Question text")
		setPropDesc(props, "options", "Selectable choices")
		setPropDesc(props, "recommended", "Recommended option label")
	case "load_skill":
		compressed.Function.Description = "Load reference skill instructions by name"
		setPropDesc(props, "name", "Skill name")
	case "load_mcp":
		compressed.Function.Description = "Connect a deferred MCP server by name"
		setPropDesc(props, "name", "MCP server name")
	case "task_status":
		compressed.Function.Description = "Check background task status and output"
		setPropDesc(props, "task_id", "Task ID")
	case "task_kill":
		compressed.Function.Description = "Terminate background task"
		setPropDesc(props, "task_id", "Task ID")
	case "create_subagent", "spawn_subagent":
		compressed.Function.Description = "Create specialized subagent"
		setPropDesc(props, "name", "Subagent name")
		setPropDesc(props, "system_prompt", "Role and instructions")
		setPropDesc(props, "skill_names", "Assigned skill names")
		setPropDesc(props, "inline_skills", "Private inline skills")
	case "remove_subagent":
		compressed.Function.Description = "Terminate subagent"
		setPropDesc(props, "name", "Subagent name")
	case "list_subagents", "swarm_topology":
		compressed.Function.Description = "List active subagents"
	case "audit_subagent", "swarm_audit":
		compressed.Function.Description = "Audit subagent execution history"
		setPropDesc(props, "name", "Subagent name")
	default:
		if strings.HasPrefix(t.Function.Name, "subagent__") {
			compressed.Function.Description = "Delegate task to subagent"
			setPropDesc(props, "prompt", "Task prompt")
		} else {
			compressed.Function.Description = truncateRunes(compressed.Function.Description, 80)
			for k, prop := range props {
				prop.Description = truncateRunes(prop.Description, 60)
				props[k] = prop
			}
		}
	}
	return compressed
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