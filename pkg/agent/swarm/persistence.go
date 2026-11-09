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
	"loop/pkg/domain/message"
)

type AgentDef = subagent.AgentDef
type AgentState = subagent.AgentState

func (mam *MultiAgentManager) getAgentsDir() (string, error) {
	return resolveAgentsDir(mam.agentsDir)
}

func resolveAgentsDir(directory string) (string, error) {
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(home, ".loop", "agents")
	}
	return filepath.Abs(directory)
}

func (mam *MultiAgentManager) getStorage() (*subagent.Storage, error) {
	dir, err := mam.getAgentsDir()
	if err != nil {
		return nil, err
	}
	return subagent.NewStorage(dir)
}

func (mam *MultiAgentManager) SaveAgentState(ma *MultiAgent, status string) error {
	ma.persistenceMu.Lock()
	defer ma.persistenceMu.Unlock()
	if ma.stateStore == nil || ma.stopped {
		return nil
	}
	ma.HistoryMu.RLock()
	history := message.CloneAll(ma.History)
	ma.HistoryMu.RUnlock()
	return ma.stateStore.SaveState(ma.Name, AgentState{
		Status:  status,
		Todos:   ma.GetTodos(),
		History: history,
	})
}

func (mam *MultiAgentManager) LoadAgentState(ma *MultiAgent) error {
	if ma.stateStore == nil {
		return nil
	}
	state, err := ma.stateStore.LoadState(ma.Name)
	if err != nil || state == nil {
		return err
	}
	ma.planMu.Lock()
	ma.todos = append([]tool.TodoItem(nil), state.Todos...)
	ma.planMu.Unlock()
	if len(state.History) > 0 {
		history, changed := message.RecoverToolHistory(state.History)
		ma.HistoryMu.Lock()
		ma.History = history
		ma.HistoryMu.Unlock()
		if changed {
			return mam.SaveAgentState(ma, "interrupted")
		}
	}
	return nil
}

func (mam *MultiAgentManager) saveAgentDef(ma *MultiAgent) error {
	ma.persistenceMu.Lock()
	defer ma.persistenceMu.Unlock()
	if ma.stateStore == nil || ma.stopped {
		return nil
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

	return ma.stateStore.SaveDefinition(AgentDef{
		Name:             ma.Name,
		SystemPrompt:     ma.SystemPrompt,
		ParentName:       parentName,
		SkillNames:       skillNames,
		LocalSkills:      localSkills,
		InheritAllSkills: &inheritAllSkills,
	})
}

func (mam *MultiAgentManager) deleteAgentDef(name string) error {
	ma := mam.Agents[name]
	if ma.stateStore == nil {
		return nil
	}
	return ma.stateStore.DeleteDefinition(name)
}

func (mam *MultiAgentManager) LoadSavedAgents() error {
	if mam.ephemeral || mam.BaseAgent == nil || mam.BaseAgent.Config == nil || mam.BaseAgent.Config.MaxSubagentDepth <= 0 {
		return nil
	}

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

	activeSkills := mam.BaseAgent.GetActiveSkills()
	if !mam.BaseAgent.HasLoadedSkills() {
		activeSkills = mam.BaseAgent.ReloadSkills()
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
		if err := mam.spawnAgentWithSkills(
			name,
			def.SystemPrompt,
			def.ParentName,
			def.SkillNames,
			def.LocalSkills,
			inheritAllSkills,
			activeSkills,
			false,
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

func (mam *MultiAgentManager) ClearAllAgents() error {
	mam.mu.Lock()
	if mam.clearing {
		mam.mu.Unlock()
		return fmt.Errorf("subagents are already being cleared")
	}
	mam.clearing = true
	agents := mam.Agents
	dir, dirErr := mam.getAgentsDir()
	ephemeral := mam.ephemeral
	mam.generation++
	mam.Agents = make(map[string]*MultiAgent)
	mam.ActiveAgent = nil
	mam.Supervisor = subagent.NewTaskSupervisor()
	if mam.BaseAgent != nil {
		mam.BaseAgent.Registry.UnregisterPrefix("subagent__")
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}
	mam.mu.Unlock()
	for _, ma := range agents {
		ma.stop()
	}
	var clearErr error
	if !ephemeral {
		clearErr = dirErr
		if clearErr == nil && dir != "" {
			clearErr = os.RemoveAll(dir)
		}
	}
	mam.mu.Lock()
	mam.clearing = false
	mam.mu.Unlock()
	return clearErr
}
