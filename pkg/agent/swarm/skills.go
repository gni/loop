package swarm

import (
	"fmt"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

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
	for _, s := range mam.BaseAgent.GetActiveSkills() {
		if s.Name == skillName {
			skillCopy := s
			targetSkill = &skillCopy
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
	if alreadyHas {
		ma.HistoryMu.Unlock()
		return nil
	}
	ma.Skills = append(ma.Skills, *targetSkill)

	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: fmt.Sprintf("loaded reference skill '%s':\n\n%s", targetSkill.Name, targetSkill.Content),
	})
	ma.HistoryMu.Unlock()

	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("save skills for subagent %q: %w", agentName, err)
	}
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
	ma.Skills = nil
	ma.LocalSkills = nil
	ma.HasAllSkills = false
	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: "cleared all loaded reference skills.",
	})
	ma.HistoryMu.Unlock()

	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("clear skills for subagent %q: %w", agentName, err)
	}
	return nil
}
