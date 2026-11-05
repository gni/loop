package agent

import (
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/resources"
)

func tpl() domaintool.PromptTemplates {
	return domaintool.TemplatesSnapshot()
}

type SystemPromptConfig struct {
	BaseInstruction   string
	WorkspaceRoot     string
	SkillsDir         string
	CompactPrompt     bool
	NativeToolSchemas bool
	GuidedMode        bool
	SkipObservation   bool
	SequentialTools   bool
	ActiveAgents      []string
	Skills            []tool.Skill
	AllSkills         []tool.Skill
	MemoryContext     string
	ProjectContext    string
	ActiveTasks       []TaskInfo
	UserGuidelines    string
	Tools             []ToolEntry
	Allowlist         []string
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
	workspaceRoot := tool.FirstNonEmpty(cfg.WorkspaceRoot, ".")
	skillsDir := tool.FirstNonEmpty(cfg.SkillsDir, "skills")

	skillsForGuidance := cfg.Skills
	if len(cfg.AllSkills) > 0 {
		skillsForGuidance = cfg.AllSkills
	}

	baseInstruction, userGuidelines := splitInstruction(cfg.BaseInstruction)
	userGuidelines = tool.FirstNonEmpty(userGuidelines, cfg.UserGuidelines)
	baseInstruction = tool.FirstNonEmpty(baseInstruction, domaintool.SystemPromptsSnapshot().DefaultIdentity)

	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	hasBash := false
	hasAskUser := false
	canSpawnSubagents := false
	for _, entry := range tools {
		if entry.Name == "bash" {
			hasBash = true
		}
		if entry.Name == "ask_user" {
			hasAskUser = true
		}
		if tool.IsSpawnTool(entry.Name) {
			canSpawnSubagents = true
		}
	}

	if !hasBash {
		if baseInstruction == domaintool.SystemPromptsSnapshot().DefaultIdentity {
			baseInstruction = "You are a software engineering assistant with tools to inspect files and edit code in the workspace."
		} else {
			baseInstruction = sanitizeNoBashInstruction(baseInstruction)
		}
	}

	var sb strings.Builder
	t := tpl()

	sb.WriteString(baseInstruction)
	sb.WriteString(fmt.Sprintf(t.CwdSection, workspaceRoot))

	var toolLines strings.Builder
	// Native tool schemas already carry names, descriptions, and arguments.
	// Keep the detailed listing for XML protocol and non-compact prompts only.
	if !cfg.CompactPrompt || !cfg.NativeToolSchemas {
		for _, entry := range tools {
			toolLines.WriteString(fmt.Sprintf(t.ToolLine, entry.Name, entry.Snippet))
		}
	}
	agentTpl := domaintool.AgentTemplatesSnapshot()
	for _, name := range cfg.ActiveAgents {
		toolLines.WriteString(fmt.Sprintf(t.SubagentLine, name, fmt.Sprintf(agentTpl.SubagentPrompt, name)))
	}
	if toolLines.Len() > 0 {
		sb.WriteString(fmt.Sprintf(t.ToolsSection, toolLines.String()))
	}

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
		if hasAskUser {
			addRule("Guided Workflow Protocol: Inspect relevant files, clarify ambiguous requirements with 'ask_user', and register an execution plan using 'todo' before mutations.")
		} else {
			addRule("Guided Workflow Protocol: Inspect relevant files and register an execution plan using 'todo' before mutations.")
		}
		addRule("Step-by-Step Progress: Execute one planned step at a time and update the 'todo' status as each step completes.")
	}

	if cfg.SequentialTools && len(tools) > 0 {
		addRule("Batch independent calls; execution is ordered. Await confirmed tool results before taking dependent actions.")
	}

	if cfg.SkipObservation {
		addRule("Observation guard is off: prior reads and revision matches are optional. Use edit for targeted changes, write for replacements; stay inside the workspace.")
	} else {
		addRule("Read existing files before edit/overwrite; new files need no read. Use edit for targeted changes, write for replacements. Paths and revisions are validated.")
	}

	for _, rule := range FormatCoreRules(workspaceRoot) {
		addRule(rule)
	}
	if !hasBash {
		addRule("Commands: Terminal command execution is unavailable. Always use available native tools.")
	}
	for _, entry := range tools {
		guidelines := entry.Guidelines
		if cfg.CompactPrompt && cfg.NativeToolSchemas {
			guidelines = domaintool.CompactToolGuidelines(entry.Name, guidelines)
		}
		for _, rule := range guidelines {
			addRule(rule)
		}
	}
	if userGuidelines != "" {
		var customLines []string
		seenCustom := make(map[string]bool)
		for _, line := range strings.Split(userGuidelines, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			lower := strings.ToLower(trimmed)
			if seenCustom[lower] {
				continue
			}
			seenCustom[lower] = true
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
			if s.DisableModelInvocation {
				continue
			}
			skillLines.WriteString(fmt.Sprintf(t.SkillLine, s.Name, s.Description))
		}
		sb.WriteString(fmt.Sprintf(t.SkillsSection, skillsDir, skillLines.String()))
	}

	var maLines strings.Builder
	if canSpawnSubagents || len(cfg.ActiveAgents) > 0 {
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
	}

	if cfg.MemoryContext != "" {
		sb.WriteString(fmt.Sprintf(t.MemorySection, cfg.MemoryContext))
	}

	if cfg.ProjectContext != "" {
		if t.ProjectSection != "" {
			sb.WriteString(fmt.Sprintf(t.ProjectSection, cfg.ProjectContext))
		} else {
			sb.WriteString(fmt.Sprintf("\n\n<project_context>\n%s\n</project_context>", cfg.ProjectContext))
		}
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

func (a *Agent) BaseSystemPromptConfig(allowlist []string, includeSubagentTools bool) SystemPromptConfig {
	if a == nil {
		return SystemPromptConfig{
			WorkspaceRoot: ".",
			SkillsDir:     "skills",
			Allowlist:     allowlist,
		}
	}

	skillsDir := "skills"
	compact := false
	instruction := ""
	if a.Config != nil {
		skillsDir = tool.FirstNonEmpty(a.Config.SkillsDir, "skills")
		compact = a.Config.CompactPrompt
		instruction = a.Config.SystemInstruction
	}

	projectCtx := ""
	if a != nil && (a.Config == nil || !a.Config.NoContextFiles) {
		projectCtx = resources.DetectProjectContext(a.WorkspaceRoot)
	}

	return SystemPromptConfig{
		BaseInstruction:   instruction,
		WorkspaceRoot:     tool.FirstNonEmpty(a.WorkspaceRoot, "."),
		SkillsDir:         skillsDir,
		CompactPrompt:     compact,
		NativeToolSchemas: a.Config == nil || !strings.EqualFold(strings.TrimSpace(a.Config.ToolProtocol), "xml"),
		GuidedMode:        a.IsGuidedMode(),
		SkipObservation:   a.Config != nil && a.Config.DisableObservationGuard,
		SequentialTools:   a.Config == nil || !a.Config.ParallelToolCalls,
		Skills:            a.GetActiveSkills(),
		MemoryContext:     a.LoadMemoryContext(),
		ProjectContext:    projectCtx,
		Allowlist:         allowlist,
		Tools:             PromptToolEntries(a.Registry, includeSubagentTools),
	}
}

func (a *Agent) GetSystemPrompt() string { return a.GetSystemPromptFor(nil) }

func (a *Agent) GetSystemPromptFor(allowlist []string) string {
	cfg := a.BaseSystemPromptConfig(allowlist, true)
	if a != nil {
		a.SpawnedAgentsMu.RLock()
		for name := range a.SpawnedAgents {
			cfg.ActiveAgents = append(cfg.ActiveAgents, name)
		}
		a.SpawnedAgentsMu.RUnlock()
		sort.Strings(cfg.ActiveAgents)

		for _, t := range a.ListTasks() {
			if t.Status == "running" {
				cfg.ActiveTasks = append(cfg.ActiveTasks, t)
			}
		}
	}
	return BuildSystemPrompt(cfg)
}

func filterToolEntries(entries []ToolEntry, allowlist []string) []ToolEntry {
	if allowlist == nil {
		return entries
	}
	out := make([]ToolEntry, 0, len(entries))
	for _, entry := range entries {
		if tool.IsAllowedTool(entry.Name, allowlist) {
			out = append(out, entry)
		}
	}
	return out
}

func PromptToolEntries(registry *tool.ToolRegistry, includeSubagentTools bool) []ToolEntry {
	if registry == nil {
		return nil
	}
	executors := registry.GetAvailableExecutors(nil)
	entries := make([]ToolEntry, 0, len(executors))
	for _, executor := range executors {
		name := executor.Name()
		if !includeSubagentTools && tool.IsSubagentTool(name) {
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
	if strings.Contains(r, "read files before editing") ||
		strings.Contains(r, "read the file first") ||
		strings.Contains(r, "read existing files before edit") ||
		(strings.Contains(r, "inspect relevant files") && strings.Contains(r, "edit")) {
		return "concept:read_before_edit"
	}
	if strings.Contains(r, "command execution is unavailable") {
		return "concept:commands_unavailable"
	}
	return r
}
