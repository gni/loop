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
	ToolProtocol      string
	NativeToolSchemas bool
	GuidedMode        bool
	SkipObservation   bool
	SequentialTools   bool
	ActiveAgents      []string
	Skills            []tool.Skill
	AllSkills         []tool.Skill
	LoadedSkills      []tool.Skill
	MemoryContext     string
	ProjectContext    string
	ActiveTasks       []TaskInfo
	UserGuidelines    string
	Tools             []ToolEntry
	Allowlist         []string
	Task              string
	ContextTokens     int
}

type ToolEntry struct {
	Name       string
	Snippet    string
	Guidelines []string
	Parameters tool.JSONSchema
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
	if before, after, ok := strings.Cut(instruction, marker); ok {
		identity = strings.TrimSpace(before)
		guidelines = strings.TrimLeft(strings.TrimSpace(after), "\n")
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
	systemPrompts := domaintool.SystemPromptsSnapshot()
	baseInstruction = tool.FirstNonEmpty(baseInstruction, systemPrompts.DefaultIdentity)

	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
	hasBash := false
	hasAskUser := false
	canSpawnSubagents := false
	toolNames := make(map[string]bool, len(tools))
	for _, entry := range tools {
		toolNames[entry.Name] = true
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
		if baseInstruction == systemPrompts.DefaultIdentity {
			baseInstruction = "You are a software engineering assistant with tools to inspect files and edit code in the workspace."
		} else {
			baseInstruction = sanitizeNoBashInstruction(baseInstruction)
		}
	}

	var sb strings.Builder
	t := tpl()

	sb.WriteString("<role>\n")
	sb.WriteString(baseInstruction)
	sb.WriteString("\n</role>")
	if workflow := strings.TrimSpace(systemPrompts.OperatingLoop); workflow != "" {
		sb.WriteString("\n\n<operating_loop>\n")
		sb.WriteString(workflow)
		sb.WriteString("\n</operating_loop>")
	}
	sb.WriteString(fmt.Sprintf(t.CwdSection, workspaceRoot))

	proto := toolProtocolFor(cfg.ToolProtocol)
	if cfg.ToolProtocol == "" && !cfg.NativeToolSchemas {
		proto = defaultXMLToolProtocol
	}
	hasActiveAgents := false
	for _, name := range cfg.ActiveAgents {
		if tool.IsAllowedTool("subagent__"+name, cfg.Allowlist) {
			hasActiveAgents = true
			break
		}
	}
	proto.FormatPromptTools(&sb, tools, toolNames, cfg)

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
			addRule("Guided Mode: Inspect files, clarify ambiguity with ask_user, and register a todo plan before mutations.")
		} else {
			addRule("Guided Mode: Inspect files and register a todo plan before mutations.")
		}
		addRule("Execute one planned step at a time and update todo status as each completes.")
	}

	if cfg.SequentialTools && len(tools) > 0 {
		addRule("Tool calls execute sequentially; await results before dependent actions.")
	}
	if canSpawnSubagents {
		addRule("Delegate only bounded multi-step work; handle single reads or commands directly.")
	}
	if toolNames["search_tools"] {
		addRule("Use search_tools to discover and enable specialized tools on demand.")
	}

	if !cfg.SkipObservation {
		addRule("Read existing files before edit/overwrite; use edit for targeted changes, write for new or full files.")
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
		for line := range strings.SplitSeq(userGuidelines, "\n") {
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

	loadedNames := make(map[string]bool, len(cfg.LoadedSkills))
	if len(cfg.LoadedSkills) > 0 {
		sb.WriteString("\n\n<loaded_skills>\nApply these assigned skills to the task; user instructions take precedence.\n")
		for _, skill := range cfg.LoadedSkills {
			key := strings.ToLower(skill.Name)
			if loadedNames[key] {
				continue
			}
			loadedNames[key] = true
			sb.WriteString(tool.FormatSkillInstructions(skill))
			sb.WriteString("\n\n")
		}
		sb.WriteString("</loaded_skills>")
	}
	if toolNames["load_skill"] && len(cfg.Skills) > 0 {
		var skillLines strings.Builder
		catalogBudget := 0
		if cfg.ContextTokens > 0 {
			catalogBudget = max(512, min(4096, cfg.ContextTokens/8))
		}
		catalog := make([]tool.Skill, 0, len(cfg.Skills))
		for _, skill := range cfg.Skills {
			if !skill.DisableModelInvocation && !loadedNames[strings.ToLower(skill.Name)] {
				catalog = append(catalog, skill)
			}
		}
		// Keep the skills catalog deterministically sorted by name so the
		// system prompt prefix does not reorder across user turns.
		skills := relevantSkills(catalog, "", catalogBudget)
		for _, s := range skills {
			if s.DisableModelInvocation || loadedNames[strings.ToLower(s.Name)] {
				continue
			}
			skillLines.WriteString(fmt.Sprintf(t.SkillLine, s.Name, s.Description))
		}
		if skillLines.Len() > 0 {
			if len(skills) < len(catalog) {
				skillLines.WriteString("More skills are available through load_skill with query; it lists matches without loading instructions.\n")
			}
			sb.WriteString(fmt.Sprintf(t.SkillsSection, skillsDir, skillLines.String()))
		}
	}

	var maLines strings.Builder
	if canSpawnSubagents || hasActiveAgents {
		maLines.WriteString(t.SubagentSkillAssignment)
		if len(skillsForGuidance) > 0 {
			validNames := make([]string, 0, len(skillsForGuidance))
			for _, s := range skillsForGuidance {
				if !s.DisableModelInvocation {
					validNames = append(validNames, s.Name)
				}
			}
			sort.Strings(validNames)
			if len(validNames) > 0 {
				maLines.WriteString(fmt.Sprintf(t.MultiAgentSkillList, strings.Join(validNames, ", ")))
			} else if t.MultiAgentNoSkills != "" {
				maLines.WriteString(t.MultiAgentNoSkills)
			}
		} else if t.MultiAgentNoSkills != "" {
			maLines.WriteString(t.MultiAgentNoSkills)
		}
		if canSpawnSubagents {
			maLines.WriteString(t.MultiAgentSpawnRules)
		}
		if trimmed := strings.TrimSpace(maLines.String()); trimmed != "" {
			sb.WriteString(fmt.Sprintf(t.MultiAgentSection, trimmed))
		}
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
	contextFiles := true
	protocolName := "json"
	skipObservation := false
	sequentialTools := true
	a.LLMProviderMu.RLock()
	if a.Config != nil {
		skillsDir = tool.FirstNonEmpty(a.Config.SkillsDir, "skills")
		compact = a.Config.CompactPrompt
		instruction = a.Config.SystemInstruction
		contextFiles = a.Config.ContextFiles
		protocolName = toolProtocolForConfig(a.Config).Name()
		skipObservation = !a.Config.FileObservationGuard
		sequentialTools = !a.Config.ParallelToolCalls
	}
	a.LLMProviderMu.RUnlock()
	nativeSchemas := toolProtocolFor(protocolName).UsesNativeSchemas()

	projectCtx := ""
	if contextFiles {
		projectCtx = resources.DetectProjectContext(a.WorkspaceRoot)
	}

	return SystemPromptConfig{
		Task:              a.knowledgeObjective(),
		ContextTokens:     a.GetEffectiveContextLimit(0),
		BaseInstruction:   instruction,
		WorkspaceRoot:     tool.FirstNonEmpty(a.WorkspaceRoot, "."),
		SkillsDir:         skillsDir,
		CompactPrompt:     compact,
		ToolProtocol:      protocolName,
		NativeToolSchemas: nativeSchemas,
		GuidedMode:        a.IsGuidedMode(),
		SkipObservation:   skipObservation,
		SequentialTools:   sequentialTools,
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
		names := make([]string, 0)
		for _, definition := range a.inferenceTools(a.Context(), allowlist) {
			names = append(names, definition.Function.Name)
		}
		cfg.Tools = filterToolEntries(cfg.Tools, names)
		a.SpawnedAgentsMu.RLock()
		for name := range a.SpawnedAgents {
			cfg.ActiveAgents = append(cfg.ActiveAgents, name)
		}
		a.SpawnedAgentsMu.RUnlock()
		sort.Strings(cfg.ActiveAgents)
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
			Parameters: executor.Definition().Function.Parameters,
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
