package tool

import (
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool/file"
)

type loadSkillTool struct{}

func NewLoadSkillTool() ToolExecutor {
	return &loadSkillTool{}
}

func (t *loadSkillTool) Name() string { return "load_skill" }

func (t *loadSkillTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Load detailed reference skill instructions")
}

func (t *loadSkillTool) Definition() Tool {
	return NewFunctionTool(
		"load_skill",
		FormatToolDescription("load_skill", "Load exact skill instructions by name. Or query available skill names/descriptions without loading bodies. Supporting files are relative to the returned skill directory."),
		map[string]SchemaProp{
			"name":  StringProp(FormatParamDescription("load_skill", "name", "The name of the skill to load (e.g. 'agent-isolation').")),
			"query": StringProp("Search available skill names/descriptions instead of loading; use name to load a match."),
		},
	)
}

func ParseSkillName(arguments string) (string, error) {
	var raw string
	var args struct {
		Name      string `json:"name"`
		Skill     string `json:"skill"`
		SkillName string `json:"skill_name"`
	}
	if err := ParseToolArguments(arguments, &args, func(s string) { raw = s }); err != nil {
		return "", fmt.Errorf("parse load_skill arguments: %w", err)
	}
	name := strings.TrimSpace(FirstNonEmpty(raw, args.Name, args.Skill, args.SkillName))
	if name == "" {
		return "", fmt.Errorf("missing required argument: skill name")
	}
	return name, nil
}

// FormatSkillInstructions is shared by model loads, manual loads and delegation.
func FormatSkillInstructions(s Skill) string {
	location := ""
	if s.Path != "" {
		location = fmt.Sprintf("\nSkill file: %s\nSkill directory: %s\nResolve supporting files relative to this directory.", s.Path, filepath.Dir(s.Path))
	}
	return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':%s\n\n%s", s.Name, location, s.Content)
}

// ReloadSkillsChecked preserves errors when the context supports checked reloads.
// Legacy tool contexts can continue implementing the smaller AgentContext API.
func ReloadSkillsChecked(ctx AgentContext) ([]Skill, error) {
	if loader, ok := ctx.(interface{ ReloadSkillsChecked() ([]Skill, error) }); ok {
		return loader.ReloadSkillsChecked()
	}
	return ctx.ReloadSkills(), nil
}

func (t *loadSkillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var search struct {
		Query string `json:"query"`
		Name  string `json:"name"`
	}
	_ = ParseToolArguments(arguments, &search, nil)
	if strings.TrimSpace(search.Query) != "" && strings.TrimSpace(search.Name) == "" {
		return searchSkills(ctx, search.Query)
	}
	name, err := ParseSkillName(arguments)
	if err != nil {
		return "", err
	}
	load := func(s Skill) (string, error) {
		if s.DisableModelInvocation {
			return "", fmt.Errorf("skill %q requires manual loading with /skills load %s", s.Name, s.Name)
		}
		return FormatSkillInstructions(s), nil
	}

	for _, s := range ctx.GetActiveSkills() {
		if strings.EqualFold(s.Name, name) {
			return load(s)
		}
	}

	skills, err := ReloadSkillsChecked(ctx)
	if err != nil {
		return "", fmt.Errorf("reload skills: %w", err)
	}
	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return load(s)
		}
	}

	return "", fmt.Errorf("skill '%s' not found", name)
}

func searchSkills(ctx AgentContext, query string) (string, error) {
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if len(query) > 256 {
		return "", fmt.Errorf("skill query must contain at most 256 bytes")
	}
	var lines []string
	skills := ctx.GetActiveSkills()
	if len(skills) == 0 {
		var err error
		skills, err = ReloadSkillsChecked(ctx)
		if err != nil {
			return "", fmt.Errorf("reload skills: %w", err)
		}
	}
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		if !strings.Contains(strings.ToLower(skill.Name+" "+skill.Description), query) {
			continue
		}
		lines = append(lines, skill.Name+": "+file.UTF8SafePrefix(skill.Description, 160))
		if len(lines) == 5 {
			break
		}
	}
	if len(lines) == 0 {
		return "No matching available skills. Use /skills to inspect configured skills.", nil
	}
	return strings.Join(lines, "\n") + "\nUp to five matches. Narrow the query or load an exact name for instructions.", nil
}
