package swarm

import (
	"fmt"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

func validateAgentLocalSkill(skill tool.Skill) (tool.Skill, error) {
	return subagent.ValidateAgentLocalSkill(skill)
}

func (mam *MultiAgentManager) ListAgentSkills(name string) ([]tool.Skill, error) {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("agent '%s' not found", name)
	}

	ma.HistoryMu.RLock()
	defer ma.HistoryMu.RUnlock()
	skills := make([]tool.Skill, len(ma.Skills))
	copy(skills, ma.Skills)
	return skills, nil
}

func (mam *MultiAgentManager) LoadAgentSkill(agentName string, skillName string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[agentName]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	var targetSkill *tool.Skill
	for _, s := range mam.BaseAgent.ActiveSkills {
		if s.Name == skillName {
			targetSkill = &s
			break
		}
	}
	if targetSkill == nil {
		return fmt.Errorf("reference skill '%s' not found in config skills directory", skillName)
	}

	ma.HistoryMu.Lock()
	alreadyHas := false
	for _, s := range ma.Skills {
		if s.Name == skillName {
			alreadyHas = true
			break
		}
	}
	if !alreadyHas {
		ma.Skills = append(ma.Skills, *targetSkill)
	}

	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", targetSkill.Name, targetSkill.Content),
	})
	ma.HistoryMu.Unlock()

	_ = mam.saveAgentDef(ma)
	return nil
}

func (mam *MultiAgentManager) ClearAgentSkills(agentName string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[agentName]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", agentName)
	}

	ma.HistoryMu.Lock()
	ma.Skills = []tool.Skill{}
	ma.LocalSkills = nil
	ma.HasAllSkills = false
	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: "cleared all loaded reference skills.",
	})
	ma.HistoryMu.Unlock()

	_ = mam.saveAgentDef(ma)
	return nil
}
