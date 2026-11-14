package swarm

import (
	"fmt"
	"strings"

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
		if strings.EqualFold(s.Name, skillName) {
			skillCopy := s
			targetSkill = &skillCopy
			break
		}
	}
	if targetSkill == nil {
		return fmt.Errorf("reference skill '%s' not found in configured skills", skillName)
	}

	ma.HistoryMu.Lock()
	assigned := false
	for index, s := range ma.Skills {
		if strings.EqualFold(s.Name, targetSkill.Name) {
			ma.Skills[index] = *targetSkill
			assigned = true
			break
		}
	}
	if !assigned {
		ma.Skills = append(ma.Skills, *targetSkill)
	}
	if ma.HasAllSkills {
		// Inheriting a catalog makes skills available; manual loading applies
		// just the selected instructions, rather than every catalog entry.
		content := tool.FormatSkillInstructions(*targetSkill)
		loaded := false
		for index := len(ma.History) - 1; index >= 0; index-- {
			if previous := ma.History[index]; strings.EqualFold(previous.SkillName, targetSkill.Name) && !previous.IsError {
				loaded = previous.Content == content
				break
			}
		}
		if !loaded {
			ma.History = append(ma.History, db.Message{Role: "system", ContextKind: "skill", SkillName: targetSkill.Name, Content: content})
		}
	}
	status := "idle"
	if ma.ActiveCancel != nil {
		status = "running"
	}
	ma.HistoryMu.Unlock()

	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("save skills for subagent %q: %w", agentName, err)
	}
	return mam.SaveAgentState(ma, status)
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
	history := ma.History[:0]
	for _, msg := range ma.History {
		if msg.SkillName != "" {
			if msg.Role != "tool" {
				continue
			}
			msg.SkillName = ""
			msg.Content = "Skill instructions cleared."
		}
		history = append(history, msg)
	}
	ma.History = history
	ma.History = append(ma.History, db.Message{
		Role:    "system",
		Content: "cleared all loaded reference skills.",
	})
	status := "idle"
	if ma.ActiveCancel != nil {
		status = "running"
	}
	ma.HistoryMu.Unlock()

	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("clear skills for subagent %q: %w", agentName, err)
	}
	return mam.SaveAgentState(ma, status)
}
