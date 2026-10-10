package tool

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
	DefaultIdentity      string   `json:"default_identity"`
	CoreRules            []string `json:"core_rules"`
	MultiAgentGuidelines string   `json:"multi_agent_guidelines"`
	CompressionPrompt    string   `json:"compression_prompt"`
}

// PromptTemplates are the render scaffolds for the system prompt sections.
// Every string the model sees is data here, not a literal in Go code.
type PromptTemplates struct {
	CwdSection              string `json:"cwd_section"`
	ToolsSection            string `json:"tools_section"`
	ToolLine                string `json:"tool_line"`
	SubagentLine            string `json:"subagent_line"`
	RulesSection            string `json:"rules_section"`
	RuleLine                string `json:"rule_line"`
	UserGuidelinesSection   string `json:"user_guidelines_section"`
	SkillsSection           string `json:"skills_section"`
	SkillsEmptySection      string `json:"skills_empty_section"`
	SkillLine               string `json:"skill_line"`
	MultiAgentSection       string `json:"multi_agent_section"`
	SubagentSkillAssignment string `json:"subagent_skill_assignment"`
	MultiAgentSkillList     string `json:"multi_agent_skill_list"`
	MultiAgentNoSkills      string `json:"multi_agent_no_skills"`
	MultiAgentSpawnRules    string `json:"multi_agent_spawn_rules"`
	MemorySection           string `json:"memory_section"`
	BackgroundTasksSection  string `json:"background_tasks_section"`
	BackgroundTaskLine      string `json:"background_task_line"`
}

// AgentToolTemplates holds the dynamic descriptions for per-agent subagent tools.
type AgentToolTemplates struct {
	SubagentPrompt      string `json:"subagent_prompt"`
	SubagentParamPrompt string `json:"subagent_param_prompt"`
	SkillNamesBase      string `json:"skill_names_base"`
	SkillNamesNone      string `json:"skill_names_none"`
	SkillNamesAvailable string `json:"skill_names_available"`
	SkillNamesUnknown   string `json:"skill_names_unknown"`
}

// RuntimeMessages are the strings the agent injects back into model history:
// defensive error advice, loop-guard warnings, recap lines, memory headers, and
// audit report scaffolds. They are instructions the model acts on, so they live
// in the adjustable catalog rather than in Go literals.
// RuntimeMessages is a key -> format-string map. Keys are looked up by name at
// call sites, so adding or changing an entry in prompts.json needs no Go change.
type RuntimeMessages map[string]string

// PromptCatalog is the full adjustable prompt data set (system prompts, tool
// prompts, and section templates).
type PromptCatalog struct {
	SystemPrompts     SystemPrompts      `json:"system_prompts"`
	ToolPrompts       map[string]ToolPrompt `json:"tool_prompts"`
	Templates         PromptTemplates    `json:"templates"`
	AgentToolTemplates AgentToolTemplates `json:"agent_tool_templates"`
	RuntimeMessages   RuntimeMessages    `json:"runtime_messages"`
}

//go:embed prompts.json
var embeddedPromptCatalog []byte

// MasterSystemPrompts, MasterToolPrompts, and MasterTemplates are the live,
// runtime-adjustable catalogs. They start from the embedded prompts.json and
// can be overridden at startup via LoadPromptCatalog.
var (
	MasterSystemPrompts      SystemPrompts
	MasterToolPrompts        map[string]ToolPrompt
	MasterTemplates          PromptTemplates
	MasterAgentTemplates     AgentToolTemplates
	MasterRuntimeMessages    RuntimeMessages
)

func init() {
	LoadPromptCatalog("")
}

// LoadPromptCatalog restores the embedded default catalog. When path is
// non-empty, the JSON file at path is layered on top of the defaults so users
// can adjust instructions without recompiling. A missing or invalid file is
// not fatal; defaults are kept.
func LoadPromptCatalog(path string) error {
	catalog, err := decodeCatalog(embeddedPromptCatalog)
	if err != nil {
		return fmt.Errorf("embedded prompt catalog is invalid: %w", err)
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				applyCatalog(catalog)
				return nil
			}
			return fmt.Errorf("failed to read prompt catalog: %w", err)
		}
		user, err := decodeCatalog(data)
		if err != nil {
			return fmt.Errorf("failed to parse prompt catalog JSON: %w", err)
		}
		mergeCatalog(&catalog, user)
	}
	applyCatalog(catalog)
	return nil
}

// applyCatalog writes the catalog into the live variables. The tool map is
// mutated in place so package-level aliases in pkg/agent keep pointing at the
// same live map.
func applyCatalog(catalog PromptCatalog) {
	MasterSystemPrompts = catalog.SystemPrompts
	MasterTemplates = catalog.Templates
	MasterAgentTemplates = catalog.AgentToolTemplates
	MasterRuntimeMessages = catalog.RuntimeMessages
	if MasterToolPrompts == nil {
		MasterToolPrompts = map[string]ToolPrompt{}
	}
	for k := range MasterToolPrompts {
		delete(MasterToolPrompts, k)
	}
	for k, v := range catalog.ToolPrompts {
		MasterToolPrompts[k] = v
	}
}

func decodeCatalog(data []byte) (PromptCatalog, error) {
	var c PromptCatalog
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.ToolPrompts == nil {
		c.ToolPrompts = map[string]ToolPrompt{}
	}
	if c.RuntimeMessages == nil {
		c.RuntimeMessages = RuntimeMessages{}
	}
	return c, nil
}

// mergeCatalog layers non-empty user values over the defaults. Tool prompts are
// merged per tool and per parameter so users can adjust a single line without
// redefining the whole entry.
func mergeCatalog(base *PromptCatalog, user PromptCatalog) {
	if user.SystemPrompts.DefaultIdentity != "" {
		base.SystemPrompts.DefaultIdentity = user.SystemPrompts.DefaultIdentity
	}
	if len(user.SystemPrompts.CoreRules) > 0 {
		base.SystemPrompts.CoreRules = user.SystemPrompts.CoreRules
	}
	if user.SystemPrompts.MultiAgentGuidelines != "" {
		base.SystemPrompts.MultiAgentGuidelines = user.SystemPrompts.MultiAgentGuidelines
	}
	if user.SystemPrompts.CompressionPrompt != "" {
		base.SystemPrompts.CompressionPrompt = user.SystemPrompts.CompressionPrompt
	}
	for name, p := range user.ToolPrompts {
		existing := base.ToolPrompts[name]
		if p.Description != "" {
			existing.Description = p.Description
		}
		if p.Snippet != "" {
			existing.Snippet = p.Snippet
		}
		if len(p.Guidelines) > 0 {
			existing.Guidelines = p.Guidelines
		}
		if existing.ParamDescriptions == nil {
			existing.ParamDescriptions = map[string]string{}
		}
		for k, v := range p.ParamDescriptions {
			existing.ParamDescriptions[k] = v
		}
		existing.Name = name
		base.ToolPrompts[name] = existing
	}
	if user.Templates.CwdSection != "" {
		base.Templates.CwdSection = user.Templates.CwdSection
	}
	if user.Templates.ToolsSection != "" {
		base.Templates.ToolsSection = user.Templates.ToolsSection
	}
	if user.Templates.ToolLine != "" {
		base.Templates.ToolLine = user.Templates.ToolLine
	}
	if user.Templates.SubagentLine != "" {
		base.Templates.SubagentLine = user.Templates.SubagentLine
	}
	if user.Templates.RulesSection != "" {
		base.Templates.RulesSection = user.Templates.RulesSection
	}
	if user.Templates.RuleLine != "" {
		base.Templates.RuleLine = user.Templates.RuleLine
	}
	if user.Templates.UserGuidelinesSection != "" {
		base.Templates.UserGuidelinesSection = user.Templates.UserGuidelinesSection
	}
	if user.Templates.SkillsSection != "" {
		base.Templates.SkillsSection = user.Templates.SkillsSection
	}
	if user.Templates.SkillsEmptySection != "" {
		base.Templates.SkillsEmptySection = user.Templates.SkillsEmptySection
	}
	if user.Templates.SkillLine != "" {
		base.Templates.SkillLine = user.Templates.SkillLine
	}
	if user.Templates.MultiAgentSection != "" {
		base.Templates.MultiAgentSection = user.Templates.MultiAgentSection
	}
	if user.Templates.SubagentSkillAssignment != "" {
		base.Templates.SubagentSkillAssignment = user.Templates.SubagentSkillAssignment
	}
	if user.Templates.MultiAgentSkillList != "" {
		base.Templates.MultiAgentSkillList = user.Templates.MultiAgentSkillList
	}
	if user.Templates.MultiAgentNoSkills != "" {
		base.Templates.MultiAgentNoSkills = user.Templates.MultiAgentNoSkills
	}
	if user.Templates.MultiAgentSpawnRules != "" {
		base.Templates.MultiAgentSpawnRules = user.Templates.MultiAgentSpawnRules
	}
	if user.Templates.MemorySection != "" {
		base.Templates.MemorySection = user.Templates.MemorySection
	}
	if user.Templates.BackgroundTasksSection != "" {
		base.Templates.BackgroundTasksSection = user.Templates.BackgroundTasksSection
	}
	if user.Templates.BackgroundTaskLine != "" {
		base.Templates.BackgroundTaskLine = user.Templates.BackgroundTaskLine
	}
	// Runtime messages merged generically by JSON key so users can adjust any
	// single advice string without listing them all.
	mergeRuntimeMessages(base.RuntimeMessages, user.RuntimeMessages)
	if user.AgentToolTemplates.SubagentPrompt != "" { base.AgentToolTemplates.SubagentPrompt = user.AgentToolTemplates.SubagentPrompt }
	if user.AgentToolTemplates.SubagentParamPrompt != "" {
		base.AgentToolTemplates.SubagentParamPrompt = user.AgentToolTemplates.SubagentParamPrompt
	}
	if user.AgentToolTemplates.SkillNamesBase != "" {
		base.AgentToolTemplates.SkillNamesBase = user.AgentToolTemplates.SkillNamesBase
	}
	if user.AgentToolTemplates.SkillNamesNone != "" {
		base.AgentToolTemplates.SkillNamesNone = user.AgentToolTemplates.SkillNamesNone
	}
	if user.AgentToolTemplates.SkillNamesAvailable != "" {
		base.AgentToolTemplates.SkillNamesAvailable = user.AgentToolTemplates.SkillNamesAvailable
	}
	if user.AgentToolTemplates.SkillNamesUnknown != "" {
		base.AgentToolTemplates.SkillNamesUnknown = user.AgentToolTemplates.SkillNamesUnknown
	}
}

// RuntimeMessage returns an adjustable runtime message template, or fallback
// when the key is missing.
func RuntimeMessage(key, fallback string) string {
	if v := MasterRuntimeMessages[key]; v != "" {
		return v
	}
	return fallback
}

// RuntimeMessagef renders a runtime message template with args. The key must
// exist in the catalog; callers pass a fallback so behavior is safe even if the
// catalog file is malformed.
func RuntimeMessagef(key, fallback string, args ...any) string {
	tplStr := MasterRuntimeMessages[key]
	if tplStr == "" {
		tplStr = fallback
	}
	return fmt.Sprintf(tplStr, args...)
}

// mergeRuntimeMessages layers non-empty user values over defaults using the
// JSON key names, so any key in the struct is adjustable without code changes
// to the merge function.
func mergeRuntimeMessages(base RuntimeMessages, user RuntimeMessages) {
	if base == nil {
		return
	}
	for k, v := range user {
		if v != "" {
			base[k] = v
		}
	}
}

// DefaultPromptCatalogPath resolves the user-editable catalog file.
func DefaultPromptCatalogPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".loop/prompts.json"
	}
	return filepath.Join(homeDir, ".loop", "prompts.json")
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
	if MasterToolPrompts == nil {
		MasterToolPrompts = map[string]ToolPrompt{}
	}
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

// FormatToolDescription returns the documented function description for a tool,
// falling back to fallbackDesc if not configured.
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
