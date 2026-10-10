package subagent

import (
	"fmt"
	"strings"
	"time"

	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

// AgentDef describes the configuration and persistent metadata of a registered subagent.
type AgentDef struct {
	Name             string             `json:"name"`
	SystemPrompt     string             `json:"system_prompt"`
	ParentName       string             `json:"parent_name"`
	SkillNames       []string           `json:"skill_names"`
	LocalSkills      []domaintool.Skill `json:"local_skills,omitempty"`
	InheritAllSkills *bool              `json:"inherit_all_skills,omitempty"`
}

// SubagentTask represents an asynchronous execution task delegated to a subagent.
type SubagentTask struct {
	ID        string    `json:"id"`
	AgentName string    `json:"agent_name"`
	Prompt    string    `json:"prompt"`
	Status    string    `json:"status"` // "pending", "running", "completed", "failed"
	Response  string    `json:"response"`
	Error     string    `json:"error"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentState records the persisted operational state and message history of a subagent.
type AgentState struct {
	Status  string            `json:"status"` // "idle", "running", "completed", "failed"
	History []message.Message `json:"history"`
}

// ValidateAgentName verifies that an agent name is valid and adheres to naming conventions.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("agent name exceeds 64 characters")
	}
	for i, r := range name {
		isAlphaNumeric := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
		if i == 0 && !isAlphaNumeric {
			return fmt.Errorf("agent name must start with a letter or number")
		}
		if !isAlphaNumeric && r != '_' && r != '-' {
			return fmt.Errorf("agent name may contain only letters, numbers, underscores, and hyphens")
		}
	}
	return nil
}

// ValidateAgentLocalSkill cleans and validates an agent-local skill declaration.
func ValidateAgentLocalSkill(skill domaintool.Skill) (domaintool.Skill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Content = strings.TrimSpace(skill.Content)
	skill.Path = ""

	if err := ValidateAgentName(skill.Name); err != nil {
		return domaintool.Skill{}, fmt.Errorf("invalid agent-local skill name %q: %w", skill.Name, err)
	}
	if skill.Content == "" {
		return domaintool.Skill{}, fmt.Errorf("agent-local skill '%s' has no instructions", skill.Name)
	}
	if len(skill.Description) > 2048 {
		return domaintool.Skill{}, fmt.Errorf("agent-local skill '%s' description exceeds 2048 bytes", skill.Name)
	}
	if len(skill.Content) > 128*1024 {
		return domaintool.Skill{}, fmt.Errorf("agent-local skill '%s' instructions exceed 128 KiB", skill.Name)
	}
	if skill.Description == "" {
		skill.Description = "Agent-local specialization."
	}

	return skill, nil
}
