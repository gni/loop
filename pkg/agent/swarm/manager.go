package swarm

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"loop/pkg/agent"
	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type MultiAgentManager struct {
	BaseAgent   *agent.Agent
	Agents      map[string]*MultiAgent
	ActiveAgent *MultiAgent
	mu          sync.RWMutex
	w           io.Writer
	theme       style.UITheme
	agentsDir   string
	Supervisor  *subagent.TaskSupervisor
}

func NewMultiAgentManager(baseAgent *agent.Agent, w io.Writer, theme style.UITheme) *MultiAgentManager {
	mam := &MultiAgentManager{
		BaseAgent:  baseAgent,
		Agents:     make(map[string]*MultiAgent),
		Supervisor: subagent.NewTaskSupervisor(),
		w:          w,
		theme:      theme,
	}

	if baseAgent != nil && baseAgent.Registry != nil {
		baseAgent.Registry.Register(&CreateSubagentTool{mam: mam})
		baseAgent.Registry.Register(&RemoveSubagentTool{mam: mam})
		baseAgent.Registry.Register(&ListSubagentsTool{mam: mam})
		baseAgent.Registry.Register(&AuditSubagentTool{mam: mam})
	}

	return mam
}

func validateAgentName(name string) error {
	return subagent.ValidateAgentName(name)
}

func (mam *MultiAgentManager) SpawnAgent(name string, systemPrompt string, parentName string, skillNames []string) error {
	return mam.spawnAgent(name, systemPrompt, parentName, skillNames, nil, len(skillNames) == 0)
}

func (mam *MultiAgentManager) spawnAgent(
	name string,
	systemPrompt string,
	parentName string,
	skillNames []string,
	localSkills []tool.Skill,
	inheritAllSkills bool,
) error {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	name = strings.TrimSpace(name)
	systemPrompt = strings.TrimSpace(systemPrompt)
	parentName = strings.TrimSpace(parentName)

	if err := validateAgentName(name); err != nil {
		return err
	}
	if systemPrompt == "" {
		return fmt.Errorf("system prompt cannot be empty")
	}
	if len(systemPrompt) > 256*1024 {
		return fmt.Errorf("system prompt exceeds 256 KiB")
	}
	if len(localSkills) > 32 {
		return fmt.Errorf("an agent cannot have more than 32 agent-local skills")
	}
	if mam.BaseAgent == nil || mam.BaseAgent.Registry == nil {
		return fmt.Errorf("multi-agent manager is not attached to an initialized base agent")
	}
	if _, exists := mam.Agents[name]; exists {
		return fmt.Errorf("agent '%s' already exists", name)
	}

	var parent *MultiAgent
	if parentName != "" {
		p, exists := mam.Agents[parentName]
		if !exists {
			return fmt.Errorf("parent agent '%s' not found", parentName)
		}
		parent = p
	}

	mam.BaseAgent.ReloadSkills()

	referenceSkills := make([]tool.Skill, 0, len(skillNames))
	assignedNames := make(map[string]struct{})
	if inheritAllSkills {
		referenceSkills = append(referenceSkills, mam.BaseAgent.ActiveSkills...)
		for _, skill := range referenceSkills {
			assignedNames[strings.ToLower(skill.Name)] = struct{}{}
		}
	} else {
		for _, sn := range skillNames {
			sn = strings.TrimSpace(sn)
			if sn == "" {
				return fmt.Errorf("reference skill name cannot be empty")
			}
			var matched *tool.Skill
			for _, s := range mam.BaseAgent.ActiveSkills {
				if strings.EqualFold(s.Name, sn) {
					skillCopy := s
					matched = &skillCopy
					break
				}
			}
			if matched == nil {
				available := make([]string, 0, len(mam.BaseAgent.ActiveSkills))
				for _, skill := range mam.BaseAgent.ActiveSkills {
					available = append(available, skill.Name)
				}
				sort.Strings(available)
				if len(available) == 0 {
					return fmt.Errorf("reference skill '%s' not found; no reference skills are installed", sn)
				}
				return fmt.Errorf("reference skill '%s' not found; available skills: %s", sn, strings.Join(available, ", "))
			}
			key := strings.ToLower(matched.Name)
			if _, exists := assignedNames[key]; exists {
				continue
			}
			assignedNames[key] = struct{}{}
			referenceSkills = append(referenceSkills, *matched)
		}
	}

	normalizedLocalSkills := make([]tool.Skill, 0, len(localSkills))
	for _, localSkill := range localSkills {
		normalized, err := validateAgentLocalSkill(localSkill)
		if err != nil {
			return err
		}
		key := strings.ToLower(normalized.Name)
		if _, exists := assignedNames[key]; exists {
			return fmt.Errorf("skill '%s' is assigned more than once", normalized.Name)
		}
		assignedNames[key] = struct{}{}
		normalizedLocalSkills = append(normalizedLocalSkills, normalized)
	}

	ma := newMultiAgentWithSkills(
		name,
		systemPrompt,
		parent,
		mam.BaseAgent,
		referenceSkills,
		normalizedLocalSkills,
		inheritAllSkills,
	)
	ma.Manager = mam

	if err := mam.LoadAgentState(ma); err != nil {
		return fmt.Errorf("load saved state for agent '%s': %w", name, err)
	}
	if err := mam.saveAgentDef(ma); err != nil {
		return fmt.Errorf("persist agent '%s': %w", name, err)
	}

	mam.Agents[name] = ma
	mam.BaseAgent.SpawnedAgentsMu.Lock()
	if mam.BaseAgent.SpawnedAgents == nil {
		mam.BaseAgent.SpawnedAgents = make(map[string]bool)
	}
	mam.BaseAgent.SpawnedAgents[name] = true
	mam.BaseAgent.SpawnedAgentsMu.Unlock()

	ma.Start(mam.w, mam.theme)

	if parent != nil {
		parent.SubagentsMu.Lock()
		parent.Subagents[name] = ma
		parent.SubagentsMu.Unlock()
	}

	toolName := fmt.Sprintf("subagent__%s", name)
	toolDef := tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        toolName,
			Description: fmt.Sprintf(tool.MasterAgentTemplates.SubagentPrompt, name),
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"prompt": tool.StringProp(tool.MasterAgentTemplates.SubagentParamPrompt),
				},
				Required: []string{"prompt"},
			},
		},
	}
	mam.BaseAgent.Registry.Register(&SubagentExecutor{
		subagent: ma,
		def:      toolDef,
	})

	return nil
}

func (mam *MultiAgentManager) SendMessage(name string, content string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	ma.Input <- db.Message{
		Role:    "user",
		Name:    "User",
		Content: content,
	}
	return nil
}

func (mam *MultiAgentManager) ListAgents() []string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()

	var names []string
	for k := range mam.Agents {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (mam *MultiAgentManager) RemoveAgent(name string) error {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	ma, exists := mam.Agents[name]
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	ma.Cancel()
	delete(mam.Agents, name)

	if mam.BaseAgent != nil {
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		if mam.BaseAgent.SpawnedAgents != nil {
			delete(mam.BaseAgent.SpawnedAgents, name)
		}
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}

	if ma.Parent != nil {
		ma.Parent.SubagentsMu.Lock()
		delete(ma.Parent.Subagents, name)
		ma.Parent.SubagentsMu.Unlock()
	}

	toolName := fmt.Sprintf("subagent__%s", name)
	mam.BaseAgent.Registry.UnregisterPrefix(toolName)

	if mam.ActiveAgent == ma {
		if ma.Parent != nil {
			mam.ActiveAgent = ma.Parent
		} else {
			mam.ActiveAgent = nil
		}
	}

	_ = mam.deleteAgentDef(name)
	return nil
}

func (mam *MultiAgentManager) ActiveAgentName() string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	if mam.ActiveAgent == nil {
		return ""
	}
	return mam.ActiveAgent.Name
}

func (mam *MultiAgentManager) HasAgent(name string) bool {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	_, exists := mam.Agents[name]
	return exists
}

func (mam *MultiAgentManager) GetParentName(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	ag, exists := mam.Agents[name]
	if !exists || ag.Parent == nil {
		return ""
	}
	return ag.Parent.Name
}

func (mam *MultiAgentManager) GetAgentSystemPrompt(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	ag, exists := mam.Agents[name]
	if !exists {
		return ""
	}
	return ag.SystemPrompt
}

func (mam *MultiAgentManager) JoinAgent(name string) bool {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	if name == "base" || name == "main" || name == "" {
		mam.ActiveAgent = nil
		return true
	}
	ag, exists := mam.Agents[name]
	if exists {
		mam.ActiveAgent = ag
		return true
	}
	return false
}
