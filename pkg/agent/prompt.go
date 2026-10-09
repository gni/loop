package agent

import (
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
)

// SystemPromptConfig encapsulates all parameters required to generate a
// consistent, deterministic system prompt across single and multi-agent roles.
type SystemPromptConfig struct {
	BaseInstruction string
	WorkspaceRoot   string
	SkillsDir       string
	CompactPrompt   bool
	ActiveAgents    []string
	Skills          []tool.Skill
	AllSkills       []tool.Skill
	MemoryContext   string
	ActiveTasks     []TaskInfo
	UserGuidelines  string
	Tools           []ToolEntry
	// Allowlist restricts which tools the agent may actually call. The catalog
	// must match it, otherwise the model is prompted with tools it cannot use.
	Allowlist []string
}

// ToolEntry is one line of the tool catalog rendered into the system prompt.
type ToolEntry struct {
	Name       string
	Snippet    string
	Guidelines []string
}

// splitInstruction separates the identity preamble from user-authored guidelines
// so both reach the model. Previously everything after "Guidelines:" was discarded.
func splitInstruction(instruction string) (identity, guidelines string) {
	const marker = "Guidelines:"
	if idx := strings.Index(instruction, marker); idx != -1 {
		identity = strings.TrimSpace(instruction[:idx])
		guidelines = strings.TrimSpace(instruction[idx+len(marker):])
		return identity, strings.TrimLeft(guidelines, "\n")
	}
	return strings.TrimSpace(instruction), ""
}

// BuildSystemPrompt constructs the complete system prompt instructions, guidelines,
// skill catalog, and multi-agent topology guidance using structured XML sections.
func BuildSystemPrompt(cfg SystemPromptConfig) string {
	workspaceRoot := cfg.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = "."
	}

	skillsDir := cfg.SkillsDir
	if skillsDir == "" {
		skillsDir = "skills"
	}

	skillsForGuidance := cfg.Skills
	if len(cfg.AllSkills) > 0 {
		skillsForGuidance = cfg.AllSkills
	}

	// User guidelines must survive: they were previously truncated away, which is
	// why instructions written in system_instruction never reached the model.
	baseInstruction, userGuidelines := splitInstruction(cfg.BaseInstruction)
	baseInstruction = strings.TrimSpace(baseInstruction)
	if userGuidelines == "" {
		userGuidelines = cfg.UserGuidelines
	}
	if baseInstruction == "" {
		baseInstruction = "You are loop, an elite autonomous software engineering harness. You solve engineering tasks with senior craft, architectural rigor, and direct working deliverables."
	}

	var sb strings.Builder

	// 1. Preamble section
	sb.WriteString(baseInstruction)

	// CWD section
	sb.WriteString("\n\n<cwd>\n")
	sb.WriteString(workspaceRoot)
	sb.WriteString("\n</cwd>\n\n")

	// 2. Tools section, generated from the registry so plugins and MCP tools are
	// never omitted from the model's view of what it can call.
	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	sb.WriteString("<tools>\n")
	for _, entry := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", entry.Name, entry.Snippet))
	}
	for _, name := range cfg.ActiveAgents {
		sb.WriteString(fmt.Sprintf("- subagent__%s: Delegate task to specialized subagent '%s'\n", name, name))
	}
	sb.WriteString("</tools>\n\n")

	// 3. Rules section: core rules, then per-tool guidelines, then user guidelines.
	sb.WriteString("<rules>\n")
	sb.WriteString(fmt.Sprintf("- Workspace root: `%s`. Any relative file paths resolve relative to this directory.\n", workspaceRoot))
	sb.WriteString("- Actions over talk: Implement code on disk directly using write and edit tools. Deliver complete working code.\n")
	sb.WriteString("- Read intent before acting: answer conversational prompts (greetings, questions, clarifications) directly; only call tools when the message asks for work on files or commands.\n")
	for _, entry := range tools {
		for _, rule := range entry.Guidelines {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}
	sb.WriteString("- Background Processes: When asked to run a command or service in the background, use the 'bash' tool with \"background\": true.\n")
	sb.WriteString("- Be concise and direct in your responses.\n")
	if userGuidelines != "" {
		sb.WriteString("\n<User Guidelines>\n")
		sb.WriteString(userGuidelines)
		sb.WriteString("\n</User Guidelines>")
	}
	sb.WriteString("</rules>")

	// 4. Skills section
	if len(cfg.Skills) > 0 {
		sb.WriteString("\n\n<skills>\n")
		sb.WriteString(fmt.Sprintf("Installed reference skills are stored in `%s`. Use the 'load_skill' tool to read detailed instructions:\n", skillsDir))
		for _, s := range cfg.Skills {
			sb.WriteString(fmt.Sprintf("- name: %s\n  description: %s\n", s.Name, s.Description))
		}
		sb.WriteString("</skills>")
	} else if cfg.CompactPrompt {
		sb.WriteString("\n\n<skills>\n")
		sb.WriteString(fmt.Sprintf("No registered reference skills are currently installed in `%s`.\n", skillsDir))
		sb.WriteString("When spawning a subagent, provide custom inline instructions using `inline_skills` or a role-tailored system prompt.\n")
		sb.WriteString("</skills>")
	}

	canSpawnSubagents := false
	for _, entry := range tools {
		if entry.Name == "create_subagent" || entry.Name == "spawn_subagent" {
			canSpawnSubagents = true
			break
		}
	}

	sb.WriteString("\n\n<multi-agent-guidelines>\n")
	sb.WriteString("- Subagent skill assignment:\n")
	if len(skillsForGuidance) > 0 {
		validNames := make([]string, 0, len(skillsForGuidance))
		for _, s := range skillsForGuidance {
			validNames = append(validNames, s.Name)
		}
		sort.Strings(validNames)
		sb.WriteString(fmt.Sprintf("  Only assign registered skills from this exact list: %s.\n", strings.Join(validNames, ", ")))
		sb.WriteString("  Do not invent reference skill names. If the capability needs custom guidance, use `inline_skills` instead.\n")
	} else {
		sb.WriteString("  No registered reference skills are currently installed. Do not invent reference skill names. Use `inline_skills` for custom instructions.\n")
	}
	if canSpawnSubagents {
		sb.WriteString("- For tasks requiring parallel or specialized work, use 'create_subagent' to create specialized agents.\n")
		sb.WriteString("- Subagents are autonomous: give each clear, bounded ownership of an explicit problem.\n")
	}
	sb.WriteString("</multi-agent-guidelines>")

	// 5. Memory section
	if cfg.MemoryContext != "" {
		sb.WriteString("\n\n<memory>\n")
		sb.WriteString(cfg.MemoryContext)
		sb.WriteString("\n</memory>")
	}

	// 6. Active tasks section
	if len(cfg.ActiveTasks) > 0 {
		sb.WriteString("\n\n<background_tasks>\n")
		for _, t := range cfg.ActiveTasks {
			sb.WriteString(fmt.Sprintf("- task_id: %s, command: `%s`, status: %s\n", t.ID, t.Command, t.Status))
		}
		sb.WriteString("</background_tasks>")
	}

	return sb.String()
}

func (a *Agent) GetSystemPrompt() string { return a.GetSystemPromptFor(nil) }

// GetSystemPromptFor builds the prompt for a specific tool allowlist so the
// advertised tool catalog matches the tools the agent can actually execute.
func (a *Agent) GetSystemPromptFor(allowlist []string) string {
	var activeAgents []string
	a.SpawnedAgentsMu.RLock()
	for name := range a.SpawnedAgents {
		activeAgents = append(activeAgents, name)
	}
	a.SpawnedAgentsMu.RUnlock()
	sort.Strings(activeAgents)

	skillsDir := "skills"
	compact := false
	instruction := ""
	if a.Config != nil {
		skillsDir = a.Config.SkillsDir
		compact = a.Config.CompactPrompt
		instruction = a.Config.SystemInstruction
	}

	var activeTasks []TaskInfo
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks = append(activeTasks, t)
		}
	}

	return BuildSystemPrompt(SystemPromptConfig{
		BaseInstruction: instruction,
		WorkspaceRoot:   a.WorkspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		ActiveAgents:    activeAgents,
		Skills:          a.ActiveSkills,
		MemoryContext:   a.LoadMemoryContext(),
		ActiveTasks:     activeTasks,
		Allowlist:       allowlist,
		Tools:           promptToolEntries(a.Registry),
	})
}

func filterToolEntries(entries []ToolEntry, allowlist []string) []ToolEntry {
	if len(allowlist) == 0 {
		return entries
	}
	allowed := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		allowed[name] = true
	}
	out := make([]ToolEntry, 0, len(entries))
	for _, entry := range entries {
		if allowed[entry.Name] || strings.HasPrefix(entry.Name, "mcp__") {
			out = append(out, entry)
		}
	}
	return out
}

// promptToolEntries renders the tool catalog from the live registry, so plugin
// and MCP tools are visible to the model instead of being hardcoded.
func promptToolEntries(registry *tool.ToolRegistry) []ToolEntry {
	if registry == nil {
		return nil
	}
	executors := registry.GetAllExecutors()
	entries := make([]ToolEntry, 0, len(executors))
	for name, executor := range executors {
		entries = append(entries, ToolEntry{
			Name:       name,
			Snippet:    tool.GetPromptSnippet(executor),
			Guidelines: tool.GetPromptGuidelines(executor),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
