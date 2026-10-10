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

	var sb strings.Builder
	t := tpl()

	// 1. Preamble section
	sb.WriteString(baseInstruction)

	// CWD section
	sb.WriteString(fmt.Sprintf(t.CwdSection, workspaceRoot))

	// 2. Tools section, generated from the registry so plugins and MCP tools are
	// never omitted from the model's view of what it can call.
	tools := filterToolEntries(cfg.Tools, cfg.Allowlist)
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
	for _, rule := range FormatCoreRules(workspaceRoot) {
		ruleLines.WriteString(fmt.Sprintf(t.RuleLine, rule))
	}
	for _, entry := range tools {
		for _, rule := range entry.Guidelines {
			ruleLines.WriteString(fmt.Sprintf(t.RuleLine, rule))
		}
	}
	if userGuidelines != "" {
		ruleLines.WriteString(fmt.Sprintf(t.UserGuidelinesSection, userGuidelines))
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
		entries = append(entries, ToolEntry{
			Name:       name,
			Snippet:    tool.GetPromptSnippet(executor),
			Guidelines: tool.GetPromptGuidelines(executor),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}
