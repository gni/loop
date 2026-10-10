package tool

import "fmt"

// ToolPrompt defines the complete prompting contract for a single agent tool.
// This includes its catalog snippet in <tools>, runtime function description,
// operational guidelines rendered into <rules>, and parameter descriptions.
type ToolPrompt struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	Snippet           string            `json:"snippet"`
	Guidelines        []string          `json:"guidelines"`
	ParamDescriptions map[string]string `json:"param_descriptions"`
}

// SystemPrompts defines the master agent identity, core operating mandates,
// and background compression prompts.
type SystemPrompts struct {
	DefaultIdentity       string   `json:"default_identity"`
	CoreRules             []string `json:"core_rules"`
	MultiAgentGuidelines  string   `json:"multi_agent_guidelines"`
	CompressionPrompt     string   `json:"compression_prompt"`
}

// MasterSystemPrompts contains the default system instructions, rules, and compression templates.
var MasterSystemPrompts = SystemPrompts{
	DefaultIdentity: "You are loop, an elite autonomous software engineering harness. You solve engineering tasks with senior craft, architectural rigor, and direct working deliverables.",
	CoreRules: []string{
		"Workspace root: `%s`. Any relative file paths resolve relative to this directory.",
		"Actions over talk: Implement code on disk directly using write and edit tools. Deliver complete working code.",
		"Tool Call Arguments: When calling file tools (especially 'write'), ALWAYS output 'path' first before 'content' in arguments JSON (e.g. `{\"path\": \"file.ext\", \"content\": \"...\"}`). Never output 'content' before 'path'.",
		"Read intent before acting: answer conversational prompts (greetings, questions, clarifications) directly; only call tools when the message asks for work on files or commands.",
		"Background Processes: When asked to run a command or service in the background, use the 'bash' tool with \"background\": true.",
		"Be concise and direct in your responses.",
	},
	CompressionPrompt: "You are a technical context compression engine. Summarize the following developer-agent conversation transcript into a dense, high-signal technical log.\n\n" +
		"Strict Requirements:\n" +
		"1. User Goals & Constraints: Exact requirements, architectural preferences, and explicit rules stated by the user.\n" +
		"2. Actions & Findings: Search results, files located or inspected, and diagnostic findings.\n" +
		"3. File Modifications: Exact paths modified or created, and key symbols added or updated.\n" +
		"4. Current State: What is complete, what failed, and immediate pending tasks.\n" +
		"5. No pleasantries, preambles, or filler. Output only the structured technical summary.\n\n" +
		"Transcript:\n%s",
}

// MasterToolPrompts is the single centralized source of truth for all tool descriptions,
// snippets, guidelines, and parameter descriptions. Modify this map to tweak or test
// any tool's prompting behavior across the entire agent harness.
var MasterToolPrompts = map[string]ToolPrompt{
	"bash": {
		Name:        "bash",
		Description: "Execute shell commands inside the workspace (builds, tests, package installation, git commands, and process management). Working directory persists across commands.",
		Snippet:     "Execute shell commands (builds, tests, git, background processes)",
		Guidelines: []string{
			"Use dedicated tools for file operations (read, edit, write, grep, find) instead of shell commands.",
			"Working directory persists across sequential commands in the session. Use 'dir' to execute in a specific directory.",
			"For background processes and long-running services, set 'background': true.",
		},
		ParamDescriptions: map[string]string{
			"command":    "The command to run in the terminal",
			"dir":        "Optional working directory in which to execute the command. Persists across commands for this session.",
			"background": "Set to true to run the command in the background as a tracked background task (for servers, daemons, or long-running tasks).",
		},
	},
	"read": {
		Name:        "read",
		Description: "Read file contents. Supports text files. Specify 'path' first before offset or limit.",
		Snippet:     "Read file contents",
		Guidelines: []string{
			"Use 'read' to examine files instead of cat or sed in bash. Do not call 'read' on directory paths; use 'list' to inspect directory trees.",
			"In tool call arguments, always output 'path' first.",
		},
		ParamDescriptions: map[string]string{
			"path":   "Path to a specific file to read (relative or absolute). Specify this parameter first. Do not pass directory paths; use 'list' to inspect directory trees.",
			"offset": "Line number to start reading from (1-indexed). Optional",
			"limit":  "Maximum number of lines to read. Optional",
		},
	},
	"write": {
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file. Specify 'path' first before 'content'. Automatically creates parent directories.",
		Snippet:     "Create or overwrite complete files",
		Guidelines: []string{
			"Use 'write' only for new files or complete rewrites. Never use after an edit mismatch.",
			"Specify the target file in 'path' and the complete file contents in 'content'.",
			"In tool call arguments, always output 'path' before 'content' to enable real-time streaming preview and syntax highlighting.",
		},
		ParamDescriptions: map[string]string{
			"path":    "Path to the target file. Specify 'path' first before 'content'.",
			"content": "Complete content to write into the file. Specify 'content' after 'path'.",
		},
	},
	"edit": {
		Name:        "edit",
		Description: "Edit a file using exact text replacement blocks. Matches unique blocks against current file contents.",
		Snippet:     "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
		Guidelines: []string{
			"Use 'edit' for precise changes (updates[].oldText must match uniquely).",
			"Keep oldText minimal (typically 2-5 lines).",
			"When modifying multiple separate locations in a file, provide multiple updates in updates[] in a single edit call.",
			"If edit reports an oldText mismatch, read the latest file and retry a smaller exact unique block. Never recover by overwriting the existing file with write.",
		},
		ParamDescriptions: map[string]string{
			"path":    "Path to the file to edit. Always specify path first.",
			"updates": "One or more targeted replacements.",
			"oldText": "Exact unique current text copied from latest read (typically 2-5 lines).",
			"newText": "The replacement text for oldText.",
		},
	},
	"grep": {
		Name:        "grep",
		Description: "Search file contents for regular expressions or literal strings across the workspace (like grep -rn). Returns matching file paths, line numbers, and lines. If pattern is empty and glob is provided, lists matching file paths. Automatically respects .gitignore and ignores dependency/build directories",
		Snippet:     "Search file contents for patterns or regular expressions",
		Guidelines: []string{
			"Use 'grep' to search definitions or references across the workspace instead of running grep/find in bash.",
		},
		ParamDescriptions: map[string]string{
			"pattern": "Regular expression pattern to search for (or literal string). If empty and glob is provided, matches file names.",
			"path":    "Directory or file to search in (default: current directory).",
			"glob":    "Optional file glob pattern to filter files by name (e.g. '*.go', '*.py').",
			"limit":   "Maximum number of matching lines to return (default: 100, max: 500).",
			"context": "Number of lines of leading and trailing context to show around each match (default: 0).",
		},
	},
	"find": {
		Name:        "find",
		Description: "Find files by glob pattern",
		Snippet:     "Find files by glob pattern",
		Guidelines: []string{
			"Use 'find' to locate files by glob pattern instead of running find in bash.",
		},
		ParamDescriptions: map[string]string{
			"pattern": "Glob pattern to match files (e.g. *.py, **/*.json)",
			"path":    "Directory to search in (default: current directory)",
			"limit":   "Maximum results to return (default: 500)",
		},
	},
	"list": {
		Name:        "list",
		Description: "List directory contents",
		Snippet:     "List directory contents",
		Guidelines: []string{
			"Use 'list' to inspect directory trees; never call 'read' on a directory path.",
			"If a listing returns nothing relevant, stop searching and answer from internal knowledge instead of re-listing.",
		},
		ParamDescriptions: map[string]string{
			"path":  "Directory to list (default: current directory)",
			"depth": "Depth to list (1 for flat, 2+ for recursive, default: 1)",
		},
	},
	"load_skill": {
		Name:        "load_skill",
		Description: "Retrieve the detailed instructions, tools, or references for a specific skill from the available skills list.",
		Snippet:     "Load detailed reference skill instructions",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{
			"name": "The name of the skill to load (e.g. 'agent-isolation').",
		},
	},
	"task_status": {
		Name:        "task_status",
		Description: "Retrieve the execution status and buffered stdout/stderr output of a background task.",
		Snippet:     "Retrieve the execution status and output of a background task",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{
			"task_id": "The ID of the background task (e.g. 'task_1').",
		},
	},
	"task_kill": {
		Name:        "task_kill",
		Description: "Terminate a running background task.",
		Snippet:     "Terminate a running background task",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{
			"task_id": "The ID of the task to terminate.",
		},
	},
	"todo": {
		Name:        "todo",
		Description: "Manage and track progress on multi-step tasks. Call this tool to initialize, update, or track your implementation plan. Send the entire updated list on every call. Exactly one task may be 'in_progress' at a time.",
		Snippet:     "Manage structured implementation plan. Whole-list replacement; exactly one task in_progress.",
		Guidelines: []string{
			"Task Planning: For complex multi-step objectives, use the 'todo' tool to initialize and update your plan. Mark the active task 'in_progress' and check off tasks as 'completed'.",
		},
		ParamDescriptions: map[string]string{
			"tasks":  "The complete updated list of tasks representing the active plan.",
			"id":     "Unique identifier for the task (e.g., 'task-1', 'step-1').",
			"task":   "Clear, concrete description of the task.",
			"status": "Task status. Exactly one task may be 'in_progress' at any given moment.",
		},
	},
	"ask_user": {
		Name:        "ask_user",
		Description: "Ask the user a structured question for clarification, disambiguation, or confirmation. Returns the user's selected choice or answer.",
		Snippet:     "Ask the user a structured question for clarification, confirmation, or selecting design alternatives",
		Guidelines: []string{
			"Call 'ask_user' when you need user input, requirements clarification, or confirmation before destructive actions.",
			"Provide clear selectable options and specify a 'recommended' choice when appropriate.",
		},
		ParamDescriptions: map[string]string{
			"question":    "The question or clarification request to present to the user.",
			"options":     "List of options for the user to select from. Can be strings or {label, description} objects.",
			"recommended": "Optional label of the recommended option.",
		},
	},
	"create_subagent": {
		Name:        "create_subagent",
		Description: "Create one specialized subagent. Use system_prompt for its role. Assign exact registered skill_names or define new private inline_skills; never invent a registered skill name.",
		Snippet:     "Create a specialized subagent",
		Guidelines: []string{
			"For tasks requiring parallel or specialized work, use 'create_subagent' to create specialized agents.",
			"Subagents are autonomous: give each clear, bounded ownership of an explicit problem.",
		},
		ParamDescriptions: map[string]string{
			"name":          "Unique name for the subagent (alphanumeric and underscores only).",
			"system_prompt": "Specific instructions and role definition for the subagent.",
			"skill_names":   "Optional list of registered skill names to attach to the subagent.",
			"inline_skills": "Optional private skills defined inline with name, description, and instructions.",
		},
	},
	"remove_subagent": {
		Name:        "remove_subagent",
		Description: "Terminate a running subagent.",
		Snippet:     "Terminate a subagent",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{
			"name": "The name of the subagent to terminate.",
		},
	},
	"list_subagents": {
		Name:        "list_subagents",
		Description: "View active subagents, their parent relationships, and their loaded skills.",
		Snippet:     "View active subagents",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{},
	},
	"audit_subagent": {
		Name:        "audit_subagent",
		Description: "Inspect the conversation history and activity log of a subagent.",
		Snippet:     "Audit subagent conversation history",
		Guidelines:  []string{},
		ParamDescriptions: map[string]string{
			"name": "The name of the subagent to audit.",
		},
	},
}

// GetToolPrompt retrieves the prompt configuration for a named tool.
// Returns an empty ToolPrompt if the tool is not found.
func GetToolPrompt(name string) ToolPrompt {
	if p, ok := MasterToolPrompts[name]; ok {
		return p
	}
	return ToolPrompt{Name: name}
}

// SetToolPrompt updates or registers a custom prompt definition for a tool.
func SetToolPrompt(p ToolPrompt) {
	MasterToolPrompts[p.Name] = p
}

// FormatParamDescription returns the documented description for a tool's parameter,
// falling back to fallbackDesc if not configured.
func FormatParamDescription(toolName, paramName, fallbackDesc string) string {
	if p, ok := MasterToolPrompts[toolName]; ok {
		if desc, found := p.ParamDescriptions[paramName]; found && desc != "" {
			return desc
		}
	}
	return fallbackDesc
}

// FormatToolDescription returns the documented function description for a tool.
func FormatToolDescription(toolName, fallbackDesc string) string {
	if p, ok := MasterToolPrompts[toolName]; ok && p.Description != "" {
		return p.Description
	}
	return fallbackDesc
}

// FormatToolSnippet returns the documented prompt catalog snippet for a tool.
func FormatToolSnippet(toolName, fallbackSnippet string) string {
	if p, ok := MasterToolPrompts[toolName]; ok && p.Snippet != "" {
		return p.Snippet
	}
	return fallbackSnippet
}

// FormatToolGuidelines returns the guidelines slice for a tool.
func FormatToolGuidelines(toolName string, fallbackGuidelines []string) []string {
	if p, ok := MasterToolPrompts[toolName]; ok && len(p.Guidelines) > 0 {
		return p.Guidelines
	}
	return fallbackGuidelines
}

// FormatCoreRules builds the master rules slice for the system prompt.
func FormatCoreRules(workspaceRoot string) []string {
	rules := make([]string, 0, len(MasterSystemPrompts.CoreRules))
	for _, r := range MasterSystemPrompts.CoreRules {
		rules = append(rules, fmt.Sprintf(r, workspaceRoot))
	}
	return rules
}
