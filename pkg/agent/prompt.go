package agent

import (
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	domaintool "loop/pkg/domain/tool"
)

// tpl returns the live render templates for prompt sections.
func tpl() domaintool.PromptTemplates {
	return domaintool.MasterTemplates
}

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
		baseInstruction = MasterSystemPrompts.DefaultIdentity
	}

	// 2. Tools section, generated from the registry so plugins and MCP tools are
	// never omitted from the model's view of what it can call.
	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	hasBash := false
	for _, entry := range tools {
		if entry.Name == "bash" {
			hasBash = true
			break
		}
	}

	if !hasBash {
		baseInstruction = sanitizeNoBashInstruction(baseInstruction)
		if userGuidelines != "" {
			userGuidelines = sanitizeNoBashInstruction(userGuidelines)
		}
	}

	var sb strings.Builder
	t := tpl()

	// 1. Preamble section
	sb.WriteString(baseInstruction)

	// CWD section
	sb.WriteString(fmt.Sprintf(t.CwdSection, workspaceRoot))

	var toolLines strings.Builder
	for _, entry := range tools {
		toolLines.WriteString(fmt.Sprintf(t.ToolLine, entry.Name, entry.Snippet))
	}
	for _, name := range cfg.ActiveAgents {
		toolLines.WriteString(fmt.Sprintf(t.SubagentLine, name, fmt.Sprintf(domaintool.MasterAgentTemplates.SubagentPrompt, name)))
	}
	sb.WriteString(fmt.Sprintf(t.ToolsSection, toolLines.String()))

	// 3. Rules section: core rules, then per-tool guidelines, then user guidelines.
	var ruleLines strings.Builder
	seenRules := make(map[string]bool)

	addRule := func(rule string) {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			return
		}
		key := normalizeRuleKey(rule)
		if seenRules[key] {
			return
		}
		seenRules[key] = true
		ruleLines.WriteString(fmt.Sprintf(t.RuleLine, rule))
	}

	for _, rule := range FormatCoreRules(workspaceRoot) {
		if !hasBash {
			if strings.Contains(rule, "Background Processes:") && strings.Contains(rule, "bash") {
				continue
			}
			if strings.Contains(rule, "Strict Tool Hierarchy:") {
				rule = "Use Available Tools: Always use available tools instead of commands."
			}
			rule = sanitizeNoBashInstruction(rule)
		}
		addRule(rule)
	}
	if !hasBash {
		addRule("Commands: Command execution is unavailable. Always use available tools instead of commands.")
	}
	for _, entry := range tools {
		for _, rule := range entry.Guidelines {
			if !hasBash {
				rule = sanitizeNoBashInstruction(rule)
			}
			addRule(rule)
		}
	}
	if userGuidelines != "" {
		if !hasBash {
			userGuidelines = sanitizeNoBashInstruction(userGuidelines)
		}
		lines := strings.Split(userGuidelines, "\n")
		var customLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			key := normalizeRuleKey(trimmed)
			if seenRules[key] {
				continue
			}
			seenRules[key] = true
			customLines = append(customLines, trimmed)
		}
		if len(customLines) > 0 {
			ruleLines.WriteString(fmt.Sprintf(t.UserGuidelinesSection, strings.Join(customLines, "\n")))
		}
	}
	sb.WriteString(fmt.Sprintf(t.RulesSection, ruleLines.String()))

	// 4. Skills section
	if len(cfg.Skills) > 0 {
		var skillLines strings.Builder
		for _, s := range cfg.Skills {
			skillLines.WriteString(fmt.Sprintf(t.SkillLine, s.Name, s.Description))
		}
		sb.WriteString(fmt.Sprintf(t.SkillsSection, skillsDir, skillLines.String()))
	} else if cfg.CompactPrompt {
		sb.WriteString(fmt.Sprintf(t.SkillsEmptySection, skillsDir))
	}

	canSpawnSubagents := false
	for _, entry := range tools {
		if entry.Name == "create_subagent" || entry.Name == "spawn_subagent" {
			canSpawnSubagents = true
			break
		}
	}

	var maLines strings.Builder
	maLines.WriteString(t.SubagentSkillAssignment)
	if len(skillsForGuidance) > 0 {
		validNames := make([]string, 0, len(skillsForGuidance))
		for _, s := range skillsForGuidance {
			validNames = append(validNames, s.Name)
		}
		sort.Strings(validNames)
		maLines.WriteString(fmt.Sprintf(t.MultiAgentSkillList, strings.Join(validNames, ", ")))
	} else {
		maLines.WriteString(t.MultiAgentNoSkills)
	}
	if canSpawnSubagents {
		maLines.WriteString(t.MultiAgentSpawnRules)
	}
	sb.WriteString(fmt.Sprintf(t.MultiAgentSection, maLines.String()))

	// 5. Memory section
	if cfg.MemoryContext != "" {
		sb.WriteString(fmt.Sprintf(t.MemorySection, cfg.MemoryContext))
	}

	// 6. Active tasks section
	if len(cfg.ActiveTasks) > 0 {
		var taskLines strings.Builder
		for _, task := range cfg.ActiveTasks {
			taskLines.WriteString(fmt.Sprintf(t.BackgroundTaskLine, task.ID, task.Command, task.Status))
		}
		sb.WriteString(fmt.Sprintf(t.BackgroundTasksSection, taskLines.String()))
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
		if registry.IsDisabled(name) {
			continue
		}
		entries = append(entries, ToolEntry{
			Name:       name,
			Snippet:    tool.GetPromptSnippet(executor),
			Guidelines: tool.GetPromptGuidelines(executor),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func sanitizeNoBashInstruction(s string) string {
	s = strings.ReplaceAll(s, ", and run shell commands in the workspace.", ".")
	s = strings.ReplaceAll(s, "and run shell commands in the workspace.", ".")
	s = strings.ReplaceAll(s, ", and run shell commands in the workspace", "")
	s = strings.ReplaceAll(s, "and run shell commands in the workspace", "")
	s = strings.ReplaceAll(s, ", and execute commands in the workspace.", ".")
	s = strings.ReplaceAll(s, "and execute commands in the workspace.", ".")
	s = strings.ReplaceAll(s, ", and execute commands in the workspace", "")
	s = strings.ReplaceAll(s, "and execute commands in the workspace", "")
	s = strings.ReplaceAll(s, "Never use bash for file operations.", "")
	s = strings.ReplaceAll(s, "Never use bash for file operations", "")
	s = strings.ReplaceAll(s, "over bash", "for file operations")
	s = strings.ReplaceAll(s, " in bash", "")
	s = strings.ReplaceAll(s, "  ", " ")
	return strings.TrimSpace(s)
}

func normalizeRuleKey(rule string) string {
	r := strings.ToLower(strings.TrimSpace(rule))
	r = strings.TrimLeft(r, "0123456789.-* ")
	r = strings.Trim(r, ".:;!?'\"`")

	// Semantic clusters for rules that appear across core rules, tool guidelines, and user configs
	if strings.Contains(r, "available tools instead of commands") ||
		(strings.Contains(r, "instead of commands") && strings.Contains(r, "tools")) ||
		strings.Contains(r, "strict tool hierarchy") ||
		strings.Contains(r, "use available tools") {
		return "concept:tools_over_commands"
	}
	if strings.Contains(r, "path") && strings.Contains(r, "content") &&
		(strings.Contains(r, "first") || strings.Contains(r, "order") || strings.Contains(r, "before")) {
		return "concept:path_first_before_content"
	}
	if strings.Contains(r, "directory") && strings.Contains(r, "read") && strings.Contains(r, "list") {
		return "concept:read_dir_use_list"
	}
	if strings.Contains(r, "actions over talk") {
		return "concept:actions_over_talk"
	}
	if strings.Contains(r, "zero-slop") || strings.Contains(r, "conciseness") {
		return "concept:conciseness"
	}
	if strings.Contains(r, "read files before editing") || strings.Contains(r, "read the file first") {
		return "concept:read_before_edit"
	}
	if strings.Contains(r, "command execution is unavailable") {
		return "concept:commands_unavailable"
	}
	return r
}
