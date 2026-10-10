package swarm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
)

type inlineSkillRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

type CreateSubagentTool struct {
	mam *MultiAgentManager
}

func (s *CreateSubagentTool) Name() string { return "create_subagent" }
func (s *CreateSubagentTool) PromptSnippet() string {
	return tool.FormatToolSnippet(s.Name(), "Create a specialized subagent")
}
func (s *CreateSubagentTool) Definition() tool.Tool {
	availableSkillNames := []string{}
	if s != nil && s.mam != nil && s.mam.BaseAgent != nil {
		seen := make(map[string]struct{}, len(s.mam.BaseAgent.ActiveSkills))
		for _, skill := range s.mam.BaseAgent.ActiveSkills {
			name := strings.TrimSpace(skill.Name)
			key := strings.ToLower(name)
			if name == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			availableSkillNames = append(availableSkillNames, name)
		}
		sort.Strings(availableSkillNames)
	}

	tpl := tool.MasterAgentTemplates
	skillNamesDescription := tpl.SkillNamesBase
	if len(availableSkillNames) == 0 {
		skillNamesDescription += tpl.SkillNamesNone
	} else {
		skillNamesDescription += fmt.Sprintf(tpl.SkillNamesAvailable, strings.Join(availableSkillNames, ", "))
	}
	skillNamesDescription += tpl.SkillNamesUnknown

	return tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        "create_subagent",
			Description: tool.FormatToolDescription("create_subagent", "Create one specialized subagent. Use system_prompt for its role. Assign exact registered skill_names or define new private inline_skills; never invent a registered skill name."),
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"name": {
						Type:        "string",
						Description: tool.FormatParamDescription("create_subagent", "name", "Unique name for the subagent."),
					},
					"system_prompt": {
						Type:        "string",
						Description: tool.FormatParamDescription("create_subagent", "system_prompt", "Role definition for the subagent."),
					},
					"skill_names": {
						Type:        "array",
						Description: skillNamesDescription,
						Items: &tool.SchemaProp{
							Type: "string",
							Enum: availableSkillNames,
						},
					},
					"inline_skills": {
						Type:        "array",
						Description: tool.FormatParamDescription("create_subagent", "inline_skills", "Optional private skills for this subagent."),
						Items: &tool.SchemaProp{
							Type: "object",
							Properties: map[string]tool.SchemaProp{
								"name":         tool.StringProp(tool.FormatParamDescription("create_subagent", "inline_skills.name", "Unique skill name.")),
								"description":  tool.StringProp(tool.FormatParamDescription("create_subagent", "inline_skills.description", "Short summary of the specialization.")),
								"instructions": tool.StringProp(tool.FormatParamDescription("create_subagent", "inline_skills.instructions", "Complete operational instructions for this skill.")),
							},
							Required: []string{"name", "instructions"},
						},
					},
				},
				Required: []string{"name", "system_prompt"},
			},
		},
	}
}

func (s *CreateSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	var args struct {
		Name         string               `json:"name"`
		AgentName    string               `json:"agent_name"`
		SubagentName string               `json:"subagent_name"`
		SystemPrompt string               `json:"system_prompt"`
		Instructions string               `json:"instructions"`
		Prompt       string               `json:"prompt"`
		Role         string               `json:"role"`
		SkillNames   []string             `json:"skill_names"`
		Skills       []string             `json:"skills"`
		InlineSkills []inlineSkillRequest `json:"inline_skills"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	args.Name = tool.FirstNonEmpty(args.Name, args.AgentName, args.SubagentName)
	args.SystemPrompt = tool.FirstNonEmpty(args.SystemPrompt, args.Instructions, args.Prompt, args.Role)
	if len(args.SkillNames) == 0 && len(args.Skills) > 0 {
		args.SkillNames = args.Skills
	}
	if strings.TrimSpace(args.Name) == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	if strings.TrimSpace(args.SystemPrompt) == "" {
		return "", fmt.Errorf("missing required argument: system_prompt")
	}

	if s == nil || s.mam == nil || s.mam.BaseAgent == nil {
		return "", fmt.Errorf("create_subagent is not attached to an initialized multi-agent manager")
	}

	var parentName string
	if mac, ok := ctx.(*multiAgentContext); ok {
		maxDepth := 0
		if s.mam.BaseAgent != nil && s.mam.BaseAgent.Config != nil {
			maxDepth = s.mam.BaseAgent.Config.MaxSubagentDepth
		}
		if err := subagent.MaxDepthAllowed(maxDepth, mac.ma.Depth()); err != nil {
			return "", fmt.Errorf("subagent '%s' is not permitted to spawn further subagents (%v); execute the task directly", mac.ma.Name, err)
		}
		parentName = mac.ma.Name
	}

	s.mam.BaseAgent.ReloadSkills()
	availableSkills := make(map[string]string, len(s.mam.BaseAgent.ActiveSkills))
	for _, skill := range s.mam.BaseAgent.ActiveSkills {
		availableSkills[strings.ToLower(strings.TrimSpace(skill.Name))] = skill.Name
	}

	localSkills := make([]tool.Skill, 0, len(args.InlineSkills)+len(args.SkillNames))
	localNames := make(map[string]struct{}, len(args.InlineSkills)+len(args.SkillNames))
	for _, inlineSkill := range args.InlineSkills {
		name := strings.TrimSpace(inlineSkill.Name)
		key := strings.ToLower(name)
		if _, exists := localNames[key]; exists {
			return "", fmt.Errorf("agent-local skill '%s' is specified more than once", name)
		}
		localNames[key] = struct{}{}
		localSkills = append(localSkills, tool.Skill{
			Name:        name,
			Description: inlineSkill.Description,
			Content:     inlineSkill.Instructions,
		})
	}

	referenceSkillNames := make([]string, 0, len(args.SkillNames))
	referenceNamesSeen := make(map[string]struct{}, len(args.SkillNames))
	convertedSkillNames := []string{}
	for _, requestedName := range args.SkillNames {
		requestedName = strings.TrimSpace(requestedName)
		key := strings.ToLower(requestedName)
		if canonicalName, exists := availableSkills[key]; exists {
			canonicalKey := strings.ToLower(canonicalName)
			if _, duplicate := referenceNamesSeen[canonicalKey]; !duplicate {
				referenceNamesSeen[canonicalKey] = struct{}{}
				referenceSkillNames = append(referenceSkillNames, canonicalName)
			}
			continue
		}

		if _, exists := localNames[key]; exists {
			continue
		}
		localNames[key] = struct{}{}
		convertedSkillNames = append(convertedSkillNames, requestedName)
		localSkills = append(localSkills, tool.Skill{
			Name:        requestedName,
			Description: fmt.Sprintf("Ad-hoc skill created for requested capability '%s'", requestedName),
			Content:     args.SystemPrompt,
		})
	}

	err := s.mam.spawnAgent(
		args.Name,
		args.SystemPrompt,
		parentName,
		referenceSkillNames,
		localSkills,
		len(referenceSkillNames) == 0 && len(localSkills) == 0,
	)
	if err != nil {
		return "", err
	}

	result := fmt.Sprintf("Subagent '%s' created successfully.", args.Name)
	if len(convertedSkillNames) > 0 {
		result += fmt.Sprintf(" Note: Unknown skill names converted to agent-local skills: %s.", strings.Join(convertedSkillNames, ", "))
	}
	return result, nil
}
