package tool

import (
	"fmt"
	"strings"
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
		FormatToolDescription("load_skill", "Retrieve the detailed instructions, tools, or references for a specific skill from the available skills list."),
		map[string]SchemaProp{
			"name": StringProp(FormatParamDescription("load_skill", "name", "The name of the skill to load (e.g. 'agent-isolation').")),
		},
		"name",
	)
}

func (t *loadSkillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var raw string
	var args struct {
		Name      string `json:"name"`
		Skill     string `json:"skill"`
		SkillName string `json:"skill_name"`
	}
	_ = ParseToolArguments(arguments, &args, func(s string) { raw = s })
	name := strings.TrimSpace(FirstNonEmpty(raw, args.Name, args.Skill, args.SkillName))
	if name == "" {
		return "", fmt.Errorf("missing required argument: skill name")
	}

	for _, s := range ctx.GetActiveSkills() {
		if strings.EqualFold(s.Name, name) {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}

	for _, s := range ctx.ReloadSkills() {
		if strings.EqualFold(s.Name, name) {
			return fmt.Sprintf("SKILL INSTRUCTIONS FOR '%s':\n\n%s", s.Name, s.Content), nil
		}
	}

	return "", fmt.Errorf("skill '%s' not found", name)
}