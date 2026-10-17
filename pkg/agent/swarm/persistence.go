package swarm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
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

func (mam *MultiAgentManager) getStorage() (*subagent.Storage, error) {
	dir, err := mam.getAgentsDir()
	if err != nil {
		return nil, err
	}
	return subagent.NewStorage(dir)
}

func (mam *MultiAgentManager) SaveAgentState(ma *MultiAgent, status string) error {
	store, err := mam.getStorage()
	if err != nil {
		return err
	}
	ma.HistoryMu.RLock()
	history := make([]db.Message, len(ma.History))
	copy(history, ma.History)
	ma.HistoryMu.RUnlock()
	return store.SaveState(ma.Name, AgentState{
		Status:  status,
		History: history,
	})
}

func (mam *MultiAgentManager) LoadAgentState(ma *MultiAgent) error {
	store, err := mam.getStorage()
	if err != nil {
		return err
	}
	state, err := store.LoadState(ma.Name)
	if err != nil || state == nil {
		return err
	}
	if len(state.History) > 0 {
		ma.HistoryMu.Lock()
		ma.History = state.History
		ma.HistoryMu.Unlock()
	}
	return nil
}

func (mam *MultiAgentManager) saveAgentDef(ma *MultiAgent) error {
	store, err := mam.getStorage()
	if err != nil {
		return err
	}

	var skillNames []string
	ma.HistoryMu.RLock()
	inheritAllSkills := ma.HasAllSkills
	localSkills := append([]tool.Skill(nil), ma.LocalSkills...)
	if !inheritAllSkills {
		localNames := make(map[string]struct{}, len(ma.LocalSkills))
		for _, localSkill := range ma.LocalSkills {
			localNames[strings.ToLower(localSkill.Name)] = struct{}{}
		}
		for _, s := range ma.Skills {
			if _, isLocal := localNames[strings.ToLower(s.Name)]; !isLocal {
				skillNames = append(skillNames, s.Name)
			}
		}
	}
	ma.HistoryMu.RUnlock()

	parentName := ""
	if ma.Parent != nil {
		parentName = ma.Parent.Name
	}

	return store.SaveDefinition(AgentDef{
		Name:             ma.Name,
		SystemPrompt:     ma.SystemPrompt,
		ParentName:       parentName,
		SkillNames:       skillNames,
		LocalSkills:      localSkills,
		InheritAllSkills: &inheritAllSkills,
	})
}

func (mam *MultiAgentManager) deleteAgentDef(name string) error {
	store, err := mam.getStorage()
	if err != nil {
		return err
	}
	return store.DeleteDefinition(name)
}

func (mam *MultiAgentManager) LoadSavedAgents() error {
	store, err := mam.getStorage()
	if err != nil {
		return err
	}
	defList, err := store.ListDefinitions()
	if err != nil || len(defList) == 0 {
		return err
	}

	defs := make(map[string]*AgentDef, len(defList))
	for i := range defList {
		if defList[i].Name != "" {
			defs[defList[i].Name] = &defList[i]
		}
	}

	spawned := make(map[string]bool, len(defs))
	spawning := make(map[string]bool, len(defs))

	var spawn func(name string) error
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
		if err := mam.spawnAgent(
			name,
			def.SystemPrompt,
			def.ParentName,
			def.SkillNames,
			def.LocalSkills,
			inheritAllSkills,
		); err != nil {
			return err
		}

		spawned[name] = true
		return nil
	}

	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	var failures []error
	for _, name := range names {
		if err := spawn(name); err != nil {
			failures = append(failures, fmt.Errorf("restore agent %q: %w", name, err))
		}
	}

	mam.mu.Lock()
	mam.ActiveAgent = nil
	mam.mu.Unlock()
	return errors.Join(failures...)
}

func (mam *MultiAgentManager) ClearAllAgents() {
	mam.mu.Lock()
	for _, ma := range mam.Agents {
		ma.CancelActiveTurn()
		if ma.Cancel != nil {
			ma.Cancel()
		}
	}
	mam.Agents = make(map[string]*MultiAgent)
	mam.ActiveAgent = nil
	mam.mu.Unlock()

	if dir, err := mam.getAgentsDir(); err == nil && dir != "" {
		_ = os.RemoveAll(dir)
	}

	if mam.BaseAgent != nil {
		mam.BaseAgent.Registry.UnregisterPrefix("subagent__")
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}
}
