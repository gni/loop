package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
)

type AgentDef = subagent.AgentDef
type AgentState = subagent.AgentState

func (mam *MultiAgentManager) getAgentsDir() (string, error) {
	if mam.agentsDir != "" {
		return mam.agentsDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".loop", "agents"), nil
}

func (mam *MultiAgentManager) SaveAgentState(ma *MultiAgent, status string) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return err
	}

	ma.HistoryMu.RLock()
	state := AgentState{
		Status:  status,
		History: ma.History,
	}
	ma.HistoryMu.RUnlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+"_state.json")
	return os.WriteFile(path, data, 0644)
}

func (mam *MultiAgentManager) LoadAgentState(ma *MultiAgent) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+"_state.json")
	if _, err := os.Stat(path); err != nil {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var state AgentState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	ma.HistoryMu.Lock()
	if len(state.History) > 0 {
		ma.History = state.History
	}
	ma.HistoryMu.Unlock()
	return nil
}

func (mam *MultiAgentManager) saveAgentDef(ma *MultiAgent) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return err
	}

	var skillNames []string
	ma.HistoryMu.RLock()
	localNames := make(map[string]struct{}, len(ma.LocalSkills))
	for _, localSkill := range ma.LocalSkills {
		localNames[strings.ToLower(localSkill.Name)] = struct{}{}
	}
	for _, s := range ma.Skills {
		if _, isLocal := localNames[strings.ToLower(s.Name)]; isLocal {
			continue
		}
		skillNames = append(skillNames, s.Name)
	}
	localSkills := append([]tool.Skill(nil), ma.LocalSkills...)
	inheritAllSkills := ma.HasAllSkills
	ma.HistoryMu.RUnlock()

	parentName := ""
	if ma.Parent != nil {
		parentName = ma.Parent.Name
	}

	if inheritAllSkills {
		skillNames = nil
	}
	def := AgentDef{
		Name:             ma.Name,
		SystemPrompt:     ma.SystemPrompt,
		ParentName:       parentName,
		SkillNames:       skillNames,
		LocalSkills:      localSkills,
		InheritAllSkills: &inheritAllSkills,
	}

	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, ma.Name+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func (mam *MultiAgentManager) deleteAgentDef(name string) error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(agentsDir, name+".json")
	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}

func (mam *MultiAgentManager) LoadSavedAgents() error {
	agentsDir, err := mam.getAgentsDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(agentsDir); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return err
	}

	defs := make(map[string]*AgentDef)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || strings.HasSuffix(entry.Name(), "_state.json") {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var def AgentDef
		if err := json.Unmarshal(data, &def); err == nil && def.Name != "" {
			defs[def.Name] = &def
		}
	}

	var spawn func(name string) error
	spawned := make(map[string]bool)
	spawning := make(map[string]bool)

	spawn = func(name string) error {
		if spawned[name] {
			return nil
		}
		if spawning[name] {
			return fmt.Errorf("circular dependency detected for agent '%s'", name)
		}
		spawning[name] = true
		defer func() { spawning[name] = false }()

		def, exists := defs[name]
		if !exists {
			return fmt.Errorf("agent definition not found for '%s'", name)
		}

		if def.ParentName != "" {
			if err := spawn(def.ParentName); err != nil {
				return err
			}
		}

		inheritAllSkills := def.SkillNames == nil
		if def.InheritAllSkills != nil {
			inheritAllSkills = *def.InheritAllSkills
		}
		err := mam.spawnAgent(
			name,
			def.SystemPrompt,
			def.ParentName,
			def.SkillNames,
			def.LocalSkills,
			inheritAllSkills,
		)
		if err != nil {
			return err
		}

		spawned[name] = true
		return nil
	}

	for name := range defs {
		_ = spawn(name)
	}

	mam.mu.Lock()
	mam.ActiveAgent = nil
	mam.mu.Unlock()

	return nil
}

func (mam *MultiAgentManager) ClearAllAgents() {
	mam.mu.Lock()
	for _, ma := range mam.Agents {
		ma.CancelActiveTurn()
	}
	mam.Agents = make(map[string]*MultiAgent)
	mam.mu.Unlock()

	home, err := os.UserHomeDir()
	if err == nil {
		os.RemoveAll(filepath.Join(home, ".loop", "agents"))
	}

	if mam.BaseAgent != nil {
		mam.BaseAgent.Registry.UnregisterPrefix("subagent__")

		mam.BaseAgent.SpawnedAgentsMu.Lock()
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}
}
