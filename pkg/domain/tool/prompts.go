package tool

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"loop/pkg/domain/document"
	"loop/pkg/storage"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type ToolPrompt struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	Snippet           string            `json:"snippet"`
	Guidelines        []string          `json:"guidelines"`
	ParamDescriptions map[string]string `json:"param_descriptions"`
}

type SystemPrompts struct {
	DefaultIdentity      string   `json:"default_identity"`
	CoreRules            []string `json:"core_rules"`
	MultiAgentGuidelines string   `json:"multi_agent_guidelines"`
	CompressionPrompt    string   `json:"compression_prompt"`
}

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

type AgentToolTemplates struct {
	SubagentPrompt      string `json:"subagent_prompt"`
	SubagentParamPrompt string `json:"subagent_param_prompt"`
	SkillNamesBase      string `json:"skill_names_base"`
	SkillNamesNone      string `json:"skill_names_none"`
	SkillNamesAvailable string `json:"skill_names_available"`
	SkillNamesUnknown   string `json:"skill_names_unknown"`
}

type RuntimeMessages map[string]string

type PromptCatalog struct {
	SystemPrompts      SystemPrompts         `json:"system_prompts"`
	ToolPrompts        map[string]ToolPrompt `json:"tool_prompts"`
	Templates          PromptTemplates       `json:"templates"`
	AgentToolTemplates AgentToolTemplates    `json:"agent_tool_templates"`
	RuntimeMessages    RuntimeMessages       `json:"runtime_messages"`
}

//go:embed prompts.json
var embeddedPromptCatalog []byte

var (
	catalogMu             sync.RWMutex
	MasterSystemPrompts   SystemPrompts
	MasterToolPrompts     map[string]ToolPrompt
	MasterTemplates       PromptTemplates
	MasterAgentTemplates  AgentToolTemplates
	MasterRuntimeMessages RuntimeMessages
	customToolPrompts     map[string]bool
)

func init() {
	_ = LoadPromptCatalog("")
}

func SystemPromptsSnapshot() SystemPrompts {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	sp := MasterSystemPrompts
	if sp.CoreRules != nil {
		sp.CoreRules = append([]string(nil), sp.CoreRules...)
	}
	return sp
}

func TemplatesSnapshot() PromptTemplates {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return MasterTemplates
}

func AgentTemplatesSnapshot() AgentToolTemplates {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return MasterAgentTemplates
}

func ToolGuidelines(name string) []string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if p, ok := MasterToolPrompts[name]; ok && p.Guidelines != nil {
		return append([]string(nil), p.Guidelines...)
	}
	return nil
}

func LoadPromptCatalog(path string) error {
	customTools := make(map[string]bool)
	catalog, err := decodeCatalog(embeddedPromptCatalog)
	if err != nil {
		return fmt.Errorf("embedded prompt catalog is invalid: %w", err)
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				catalogMu.Lock()
				applyCatalogLocked(catalog)
				customToolPrompts = customTools
				catalogMu.Unlock()
				return nil
			}
			return fmt.Errorf("failed to read prompt catalog: %w", err)
		}
		user, err := decodeCatalog(data)
		if err != nil {
			return fmt.Errorf("failed to parse prompt catalog JSON: %w", err)
		}
		for name, prompt := range user.ToolPrompts {
			if prompt.Description != "" || prompt.Snippet != "" || len(prompt.Guidelines) > 0 || len(prompt.ParamDescriptions) > 0 {
				customTools[name] = true
			}
		}
		mergeCatalog(&catalog, user)
	}
	catalogMu.Lock()
	applyCatalogLocked(catalog)
	customToolPrompts = customTools
	catalogMu.Unlock()
	return nil
}

// hasToolPromptOverride protects explicit catalog instructions from compaction.
func hasToolPromptOverride(name string) bool {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return customToolPrompts[name]
}

func applyCatalogLocked(catalog PromptCatalog) {
	MasterSystemPrompts = catalog.SystemPrompts
	MasterTemplates = catalog.Templates
	MasterAgentTemplates = catalog.AgentToolTemplates
	MasterRuntimeMessages = catalog.RuntimeMessages
	nextTools := make(map[string]ToolPrompt, len(catalog.ToolPrompts))
	for k, v := range catalog.ToolPrompts {
		nextTools[k] = v
	}
	MasterToolPrompts = nextTools
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

func overrideString(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

func mergeCatalog(base *PromptCatalog, user PromptCatalog) {
	overrideString(&base.SystemPrompts.DefaultIdentity, user.SystemPrompts.DefaultIdentity)
	if len(user.SystemPrompts.CoreRules) > 0 {
		base.SystemPrompts.CoreRules = user.SystemPrompts.CoreRules
	}
	overrideString(&base.SystemPrompts.MultiAgentGuidelines, user.SystemPrompts.MultiAgentGuidelines)
	overrideString(&base.SystemPrompts.CompressionPrompt, user.SystemPrompts.CompressionPrompt)

	for name, p := range user.ToolPrompts {
		existing := base.ToolPrompts[name]
		overrideString(&existing.Description, p.Description)
		overrideString(&existing.Snippet, p.Snippet)
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

	overrideString(&base.Templates.CwdSection, user.Templates.CwdSection)
	overrideString(&base.Templates.ToolsSection, user.Templates.ToolsSection)
	overrideString(&base.Templates.ToolLine, user.Templates.ToolLine)
	overrideString(&base.Templates.SubagentLine, user.Templates.SubagentLine)
	overrideString(&base.Templates.RulesSection, user.Templates.RulesSection)
	overrideString(&base.Templates.RuleLine, user.Templates.RuleLine)
	overrideString(&base.Templates.UserGuidelinesSection, user.Templates.UserGuidelinesSection)
	overrideString(&base.Templates.SkillsSection, user.Templates.SkillsSection)
	overrideString(&base.Templates.SkillsEmptySection, user.Templates.SkillsEmptySection)
	overrideString(&base.Templates.SkillLine, user.Templates.SkillLine)
	overrideString(&base.Templates.MultiAgentSection, user.Templates.MultiAgentSection)
	overrideString(&base.Templates.SubagentSkillAssignment, user.Templates.SubagentSkillAssignment)
	overrideString(&base.Templates.MultiAgentSkillList, user.Templates.MultiAgentSkillList)
	overrideString(&base.Templates.MultiAgentNoSkills, user.Templates.MultiAgentNoSkills)
	overrideString(&base.Templates.MultiAgentSpawnRules, user.Templates.MultiAgentSpawnRules)
	overrideString(&base.Templates.MemorySection, user.Templates.MemorySection)
	overrideString(&base.Templates.BackgroundTasksSection, user.Templates.BackgroundTasksSection)
	overrideString(&base.Templates.BackgroundTaskLine, user.Templates.BackgroundTaskLine)

	mergeRuntimeMessages(base.RuntimeMessages, user.RuntimeMessages)

	overrideString(&base.AgentToolTemplates.SubagentPrompt, user.AgentToolTemplates.SubagentPrompt)
	overrideString(&base.AgentToolTemplates.SubagentParamPrompt, user.AgentToolTemplates.SubagentParamPrompt)
	overrideString(&base.AgentToolTemplates.SkillNamesBase, user.AgentToolTemplates.SkillNamesBase)
	overrideString(&base.AgentToolTemplates.SkillNamesNone, user.AgentToolTemplates.SkillNamesNone)
	overrideString(&base.AgentToolTemplates.SkillNamesAvailable, user.AgentToolTemplates.SkillNamesAvailable)
	overrideString(&base.AgentToolTemplates.SkillNamesUnknown, user.AgentToolTemplates.SkillNamesUnknown)
}

func RuntimeMessage(key, fallback string) string {
	catalogMu.RLock()
	v := MasterRuntimeMessages[key]
	catalogMu.RUnlock()
	if v != "" {
		return v
	}
	return fallback
}

func RuntimeMessagef(key, fallback string, args ...any) string {
	catalogMu.RLock()
	tplStr := MasterRuntimeMessages[key]
	catalogMu.RUnlock()
	if tplStr == "" {
		tplStr = fallback
	}
	return fmt.Sprintf(tplStr, args...)
}

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

func DefaultPromptCatalogPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".loop/prompts.json"
	}
	return filepath.Join(homeDir, ".loop", "prompts.json")
}

func FormatParamDescription(toolName, paramName, fallbackDesc string) string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if p, ok := MasterToolPrompts[toolName]; ok {
		if desc, found := p.ParamDescriptions[paramName]; found && desc != "" {
			return desc
		}
	}
	return fallbackDesc
}

func FormatToolDescription(toolName, fallbackDesc string) string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if p, ok := MasterToolPrompts[toolName]; ok && p.Description != "" {
		return p.Description
	}
	return fallbackDesc
}

func FormatToolSnippet(toolName, fallbackSnippet string) string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if p, ok := MasterToolPrompts[toolName]; ok && p.Snippet != "" {
		return p.Snippet
	}
	return fallbackSnippet
}

func FormatToolGuidelines(toolName string, fallbackGuidelines []string) []string {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if p, ok := MasterToolPrompts[toolName]; ok && len(p.Guidelines) > 0 {
		return append([]string(nil), p.Guidelines...)
	}
	return fallbackGuidelines
}

func FormatCoreRules(workspaceRoot string) []string {
	catalogMu.RLock()
	coreRules := append([]string(nil), MasterSystemPrompts.CoreRules...)
	catalogMu.RUnlock()
	rules := make([]string, 0, len(coreRules))
	for _, r := range coreRules {
		if strings.Contains(r, "%s") {
			rules = append(rules, fmt.Sprintf(r, workspaceRoot))
		} else {
			rules = append(rules, r)
		}
	}
	return rules
}

type PromptTemplate struct {
	Name        string
	Description string
	Content     string
	FilePath    string
}

func ParseCommandArgs(argsString string) []string {
	var args []string
	var current string
	var inQuote rune
	for _, c := range argsString {
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			} else {
				current += string(c)
			}
		} else if c == '"' || c == '\'' {
			inQuote = c
		} else if unicode.IsSpace(c) {
			if current != "" {
				args = append(args, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		args = append(args, current)
	}
	return args
}

var argPattern = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

func SubstituteArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	return argPattern.ReplaceAllStringFunc(content, func(m string) string {
		parts := argPattern.FindStringSubmatch(m)
		if parts[1] != "" {
			var v string
			if parts[1] == "@" || parts[1] == "ARGUMENTS" {
				v = all
			} else if n, _ := strconv.Atoi(parts[1]); n > 0 && n <= len(args) {
				v = args[n-1]
			}
			if v == "" {
				return parts[2]
			}
			return v
		}
		if parts[3] != "" {
			start := 0
			if n, _ := strconv.Atoi(parts[3]); n > 0 {
				start = n - 1
			}
			if start >= len(args) {
				return ""
			}
			slice := args[start:]
			if parts[4] != "" {
				if l, _ := strconv.Atoi(parts[4]); l >= 0 && l < len(slice) {
					slice = slice[:l]
				}
			}
			return strings.Join(slice, " ")
		}
		switch parts[5] {
		case "@", "ARGUMENTS":
			return all
		default:
			if n, _ := strconv.Atoi(parts[5]); n > 0 && n <= len(args) {
				return args[n-1]
			}
			return ""
		}
	})
}

type cachedTemplateFile struct {
	modTime  time.Time
	size     int64
	template PromptTemplate
}

type cachedTemplateDir struct {
	dirModTime time.Time
	checkedAt  time.Time
	files      map[string]cachedTemplateFile
	templates  []PromptTemplate
}

var (
	templateCacheMu sync.Mutex
	templateCache   = make(map[string]*cachedTemplateDir)
)

func LoadPromptTemplates(dir string) []PromptTemplate {
	if dir == "" {
		return nil
	}
	cleanDir := filepath.Clean(dir)
	now := time.Now()

	templateCacheMu.Lock()
	cached := templateCache[cleanDir]
	if cached != nil && now.Sub(cached.checkedAt) < 500*time.Millisecond {
		out := append([]PromptTemplate(nil), cached.templates...)
		templateCacheMu.Unlock()
		return out
	}
	templateCacheMu.Unlock()

	dirInfo, err := os.Stat(cleanDir)
	if err != nil || !dirInfo.IsDir() {
		templateCacheMu.Lock()
		delete(templateCache, cleanDir)
		templateCacheMu.Unlock()
		return nil
	}

	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil
	}

	templateCacheMu.Lock()
	var prevFiles map[string]cachedTemplateFile
	if existing := templateCache[cleanDir]; existing != nil {
		prevFiles = existing.files
	}
	templateCacheMu.Unlock()

	nextFiles := make(map[string]cachedTemplateFile, len(entries))
	var out []PromptTemplate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(cleanDir, e.Name())
		info, infoErr := e.Info()
		if infoErr == nil && prevFiles != nil {
			if prev, ok := prevFiles[e.Name()]; ok && prev.modTime.Equal(info.ModTime()) && prev.size == info.Size() {
				nextFiles[e.Name()] = prev
				out = append(out, prev.template)
				continue
			}
		}
		raw, err := storage.ReadFile(path, 512<<10)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		desc, body := parseTemplateFrontmatter(string(raw))
		tpl := PromptTemplate{Name: name, Description: desc, Content: body, FilePath: path}
		if infoErr == nil {
			nextFiles[e.Name()] = cachedTemplateFile{modTime: info.ModTime(), size: info.Size(), template: tpl}
		}
		out = append(out, tpl)
	}

	templateCacheMu.Lock()
	templateCache[cleanDir] = &cachedTemplateDir{
		dirModTime: dirInfo.ModTime(),
		checkedAt:  now,
		files:      nextFiles,
		templates:  append([]PromptTemplate(nil), out...),
	}
	templateCacheMu.Unlock()
	return out
}

func parseTemplateFrontmatter(content string) (string, string) {
	metadata, body, err := document.Frontmatter(content)
	if err != nil {
		return "", content
	}
	return metadata["description"], body
}
