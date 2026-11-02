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
	ephemeral   bool
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
		if baseAgent.Config == nil || baseAgent.Config.MaxSubagentDepth <= 0 {
			baseAgent.Registry.Disable("create_subagent", "remove_subagent", "list_subagents", "audit_subagent")
		}
	}

	return mam
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
	return mam.spawnAgentWithSkills(name, systemPrompt, parentName, skillNames, localSkills, inheritAllSkills, nil, true)
}

func (mam *MultiAgentManager) spawnAgentWithSkills(
	name string,
	systemPrompt string,
	parentName string,
	skillNames []string,
	localSkills []tool.Skill,
	inheritAllSkills bool,
	preloadedSkills []tool.Skill,
	persistDef bool,
) error {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	if mam.BaseAgent == nil || mam.BaseAgent.Config == nil || mam.BaseAgent.Config.MaxSubagentDepth <= 0 {
		return fmt.Errorf("subagents are disabled; set max_subagent_depth to a positive number")
	}
	name = strings.TrimSpace(name)
	systemPrompt = strings.TrimSpace(systemPrompt)
	parentName = strings.TrimSpace(parentName)

	if err := subagent.ValidateAgentName(name); err != nil {
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
		if err := subagent.MaxDepthAllowed(mam.BaseAgent.Config.MaxSubagentDepth, p.Depth()+1); err != nil {
			return fmt.Errorf("subagent nesting disallowed: %w", err)
		}
		parent = p
	}

	activeSkills := preloadedSkills
	if activeSkills == nil {
		activeSkills = mam.BaseAgent.ReloadSkills()
	}

	referenceSkills := make([]tool.Skill, 0, len(skillNames))
	assignedNames := make(map[string]struct{})
	if inheritAllSkills {
		referenceSkills = append(referenceSkills, activeSkills...)
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
			for _, s := range activeSkills {
				if strings.EqualFold(s.Name, sn) {
					skillCopy := s
					matched = &skillCopy
					break
				}
			}
			if matched == nil {
				available := make([]string, 0, len(activeSkills))
				for _, skill := range activeSkills {
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
		normalized, err := subagent.ValidateAgentLocalSkill(localSkill)
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
	if persistDef {
		if err := mam.saveAgentDef(ma); err != nil {
			return fmt.Errorf("persist agent '%s': %w", name, err)
		}
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

	tpl := tool.AgentTemplatesSnapshot()
	toolName := fmt.Sprintf("subagent__%s", name)
	toolDef := tool.Tool{
		Type: "function",
		Function: tool.FunctionDefinition{
			Name:        toolName,
			Description: fmt.Sprintf(tpl.SubagentPrompt, name),
			Parameters: tool.JSONSchema{
				Type: "object",
				Properties: map[string]tool.SchemaProp{
					"prompt": tool.StringProp(tpl.SubagentParamPrompt),
				},
				Required: []string{"prompt"},
			},
		},
	}
	mam.BaseAgent.Registry.Register(&SubagentExecutor{
		subagent: ma,
		def:      toolDef,
	})
	if mam.BaseAgent.Config.MaxSubagentDepth <= 0 {
		mam.BaseAgent.Registry.Disable(toolName)
	}

	return nil
}

func (mam *MultiAgentManager) SendMessage(name string, content string) error {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}

	select {
	case <-ma.Context.Done():
		return fmt.Errorf("agent '%s' is no longer running", name)
	case ma.Input <- db.Message{
		Role:    "user",
		Name:    "User",
		Content: content,
	}:
		return nil
	}
}

func (mam *MultiAgentManager) ListAgents() []string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()

	names := make([]string, 0, len(mam.Agents))
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

	// The persisted definition must be removed before changing in-memory state.
	// Otherwise a failed disk removal would resurrect this agent on restart.
	if err := mam.deleteAgentDef(name); err != nil {
		return fmt.Errorf("remove saved subagent %q: %w", name, err)
	}

	ma.CancelActiveTurn()
	if ma.Cancel != nil {
		ma.Cancel()
	}
	ma.KillTasks()
	delete(mam.Agents, name)

	if mam.BaseAgent != nil {
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		delete(mam.BaseAgent.SpawnedAgents, name)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}

	if ma.Parent != nil {
		ma.Parent.SubagentsMu.Lock()
		delete(ma.Parent.Subagents, name)
		ma.Parent.SubagentsMu.Unlock()
	}

	ma.SubagentsMu.Lock()
	for childName, child := range ma.Subagents {
		if child != nil {
			child.Parent = ma.Parent
			if ma.Parent != nil {
				ma.Parent.SubagentsMu.Lock()
				ma.Parent.Subagents[childName] = child
				ma.Parent.SubagentsMu.Unlock()
			}
		}
	}
	ma.Subagents = make(map[string]*MultiAgent)
	ma.SubagentsMu.Unlock()

	if mam.BaseAgent != nil && mam.BaseAgent.Registry != nil {
		mam.BaseAgent.Registry.Unregister(fmt.Sprintf("subagent__%s", name))
	}

	if mam.ActiveAgent == ma {
		mam.ActiveAgent = ma.Parent
	}

	return nil
}

// CurrentActiveAgent returns the selected agent without exposing the manager's
// selection field to unsynchronized reads. The agent remains valid if another
// goroutine switches sessions after this snapshot.
func (mam *MultiAgentManager) CurrentActiveAgent() *MultiAgent {
	if mam == nil {
		return nil
	}
	mam.mu.RLock()
	active := mam.ActiveAgent
	mam.mu.RUnlock()
	return active
}

// ActiveHistorySnapshot copies the selected agent's history while holding its
// history lock. Rendering must not traverse a live, mutable history slice.
func (mam *MultiAgentManager) ActiveHistorySnapshot() ([]db.Message, bool) {
	active := mam.CurrentActiveAgent()
	if active == nil {
		return nil, false
	}
	active.HistoryMu.RLock()
	history := append([]db.Message(nil), active.History...)
	active.HistoryMu.RUnlock()
	return history, true
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
	if ag, exists := mam.Agents[name]; exists {
		mam.ActiveAgent = ag
		return true
	}
	return false
}
