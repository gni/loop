package agent

import (
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	domaintool "loop/pkg/domain/tool"
)

func tpl() domaintool.PromptTemplates {
	return domaintool.MasterTemplates
}

type SystemPromptConfig struct {
	BaseInstruction string
	WorkspaceRoot   string
	SkillsDir       string
	CompactPrompt   bool
	GuidedMode      bool
	ActiveAgents    []string
	Skills          []tool.Skill
	AllSkills       []tool.Skill
	MemoryContext   string
	ActiveTasks     []TaskInfo
	UserGuidelines  string
	Tools           []ToolEntry
	Allowlist       []string
}

type ToolEntry struct {
	Name       string
	Snippet    string
	Guidelines []string
}

var noBashReplacer = strings.NewReplacer(
	", and run shell commands in the workspace.", ".",
	"and run shell commands in the workspace.", ".",
	", and run shell commands in the workspace", "",
	"and run shell commands in the workspace", "",
	", and execute commands in the workspace.", ".",
	"and execute commands in the workspace.", ".",
	", and execute commands in the workspace", "",
	"and execute commands in the workspace", "",
	"Never use bash for file operations.", "",
	"Never use bash for file operations", "",
	"over bash", "for file operations",
	" in bash", "",
)

func splitInstruction(instruction string) (identity, guidelines string) {
	const marker = "Guidelines:"
	if idx := strings.Index(instruction, marker); idx != -1 {
		identity = strings.TrimSpace(instruction[:idx])
		guidelines = strings.TrimLeft(strings.TrimSpace(instruction[idx+len(marker):]), "\n")
		return identity, guidelines
	}
	return strings.TrimSpace(instruction), ""
}

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

	baseInstruction, userGuidelines := splitInstruction(cfg.BaseInstruction)
	if userGuidelines == "" {
		userGuidelines = cfg.UserGuidelines
	}
	if baseInstruction == "" {
		baseInstruction = MasterSystemPrompts.DefaultIdentity
	}

	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	hasBash := false
	canSpawnSubagents := false
	for _, entry := range tools {
		if entry.Name == "bash" {
			hasBash = true
		}
		if entry.Name == "create_subagent" || entry.Name == "spawn_subagent" {
			canSpawnSubagents = true
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

	sb.WriteString(baseInstruction)
	sb.WriteString(fmt.Sprintf(t.CwdSection, workspaceRoot))

	var toolLines strings.Builder
	for _, entry := range tools {
		toolLines.WriteString(fmt.Sprintf(t.ToolLine, entry.Name, entry.Snippet))
	}
	for _, name := range cfg.ActiveAgents {
		toolLines.WriteString(fmt.Sprintf(t.SubagentLine, name, fmt.Sprintf(domaintool.MasterAgentTemplates.SubagentPrompt, name)))
	}
	sb.WriteString(fmt.Sprintf(t.ToolsSection, toolLines.String()))

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

	if cfg.GuidedMode {
		addRule("Guided Workflow Protocol: You are operating in Guided Mode. Never call mutating tools ('write', 'edit', 'bash', 'create_subagent') before completing three steps: (1) inspect relevant files using 'read', 'grep', 'find', or 'list', (2) clarify any ambiguous requirements using 'ask_user', and (3) register a concrete step-by-step execution plan using 'todo'.")
		addRule("Step-by-Step Progress: Execute one planned step at a time and update the 'todo' status as each step completes.")
	}

	for _, rule := range FormatCoreRules(workspaceRoot) {
		if cfg.GuidedMode && strings.Contains(rule, "Actions Over Talk") {
			continue
		}
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

	if len(cfg.Skills) > 0 {
		var skillLines strings.Builder
		for _, s := range cfg.Skills {
			skillLines.WriteString(fmt.Sprintf(t.SkillLine, s.Name, s.Description))
		}
		sb.WriteString(fmt.Sprintf(t.SkillsSection, skillsDir, skillLines.String()))
	} else if cfg.CompactPrompt {
		sb.WriteString(fmt.Sprintf(t.SkillsEmptySection, skillsDir))
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

	if cfg.MemoryContext != "" {
		sb.WriteString(fmt.Sprintf(t.MemorySection, cfg.MemoryContext))
	}

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
		GuidedMode:      a.IsGuidedMode(),
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
	cleaned := noBashReplacer.Replace(s)
	return strings.TrimSpace(strings.ReplaceAll(cleaned, "  ", " "))
}

func normalizeRuleKey(rule string) string {
	r := strings.ToLower(strings.TrimSpace(rule))
	r = strings.TrimLeft(r, "0123456789.-* ")
	r = strings.Trim(r, ".:;!?'\"`")

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