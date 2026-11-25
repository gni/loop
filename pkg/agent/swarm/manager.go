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
	"loop/pkg/domain/message"
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
	staging     bool
	closed      bool
	clearing    bool
	generation  uint64
	pending     map[string]bool
}

func NewMultiAgentManager(baseAgent *agent.Agent, w io.Writer, theme style.UITheme) *MultiAgentManager {
	mam := &MultiAgentManager{
		BaseAgent:  baseAgent,
		Agents:     make(map[string]*MultiAgent),
		Supervisor: subagent.NewTaskSupervisor(),
		w:          w,
		theme:      theme,
		pending:    make(map[string]bool),
	}

	if baseAgent != nil && baseAgent.Registry != nil {
		baseAgent.Registry.Register(&CreateSubagentTool{mam: mam})
		baseAgent.Registry.Register(&RemoveSubagentTool{mam: mam})
		baseAgent.Registry.Register(&ListSubagentsTool{mam: mam})
		baseAgent.Registry.Register(&AuditSubagentTool{mam: mam})
		if baseAgent.LiveSettings().MaxSubagentDepth <= 0 {
			baseAgent.Registry.Disable("create_subagent", "remove_subagent", "list_subagents", "inspect_subagent")
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
	return mam.spawnAgentWithSkillsFor(nil, name, systemPrompt, parentName, skillNames, localSkills, inheritAllSkills, preloadedSkills, persistDef)
}

func (mam *MultiAgentManager) spawnAgentWithSkillsFor(
	ctx tool.AgentContext,
	name, systemPrompt, parentName string,
	skillNames []string,
	localSkills []tool.Skill,
	inheritAllSkills bool,
	preloadedSkills []tool.Skill,
	persistDef bool,
) error {
	rawName := strings.TrimSpace(name)
	name = rawName
	if persistDef && rawName != "" {
		normalized := subagent.NormalizeAgentName(rawName)
		if normalized == "" {
			return fmt.Errorf("invalid agent name %q: must contain at least one letter or number", rawName)
		}
		name = normalized
	}
	systemPrompt = strings.TrimSpace(systemPrompt)
	parentName = strings.TrimSpace(parentName)

	maxDepth := 0
	if mam.BaseAgent != nil {
		maxDepth = mam.BaseAgent.LiveSettings().MaxSubagentDepth
	}
	if maxDepth <= 0 {
		return fmt.Errorf("subagents are disabled; set max_subagent_depth to a positive number")
	}
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

	mam.mu.Lock()
	if err := mam.checkMutationContextLocked(ctx); err != nil {
		mam.mu.Unlock()
		return err
	}
	if persistDef {
		if existingName, _, exists := mam.resolveAgentLocked(name); exists {
			mam.mu.Unlock()
			return fmt.Errorf("agent '%s' already exists", existingName)
		}
	} else if _, exists := mam.Agents[name]; exists {
		mam.mu.Unlock()
		return fmt.Errorf("agent '%s' already exists", name)
	}
	if mam.pending[name] {
		mam.mu.Unlock()
		return fmt.Errorf("agent '%s' is already being modified", name)
	}

	var parent *MultiAgent
	if parentName != "" {
		resolvedParent, p, exists := mam.resolveAgentLocked(parentName)
		if !exists {
			mam.mu.Unlock()
			return fmt.Errorf("parent agent '%s' not found", parentName)
		}
		parentName = resolvedParent
		if mam.pending[parentName] {
			mam.mu.Unlock()
			return fmt.Errorf("parent agent '%s' is currently being modified", parentName)
		}
		if err := subagent.MaxDepthAllowed(maxDepth, p.Depth()+1); err != nil {
			mam.mu.Unlock()
			return fmt.Errorf("subagent nesting disallowed: %w", err)
		}
		parent = p
	}

	if mam.pending == nil {
		mam.pending = make(map[string]bool)
	}
	mam.pending[name] = true
	ephemeral := mam.ephemeral
	agentsDir := mam.agentsDir
	if mam.Supervisor == nil {
		mam.Supervisor = subagent.NewTaskSupervisor()
	}
	supervisor := mam.Supervisor
	gen := mam.generation
	mam.mu.Unlock()

	pendingReserved := true
	registered := false
	persistedDef := false
	var ma *MultiAgent
	defer func() {
		if pendingReserved {
			mam.mu.Lock()
			delete(mam.pending, name)
			mam.mu.Unlock()
		}
		if !registered && ma != nil {
			ma.Cancel()
			if persistedDef && ma.stateStore != nil {
				_ = ma.stateStore.DeleteDefinition(name)
			}
		}
	}()

	// Blocking disk I/O runs outside mam.mu while name is reserved in mam.pending.
	activeSkills := preloadedSkills
	if activeSkills == nil {
		var err error
		activeSkills, err = mam.BaseAgent.ReloadSkillsChecked()
		if err != nil {
			return fmt.Errorf("reload skills: %w", err)
		}
	}

	referenceSkills := make([]tool.Skill, 0, len(skillNames))
	assignedNames := make(map[string]struct{})
	if inheritAllSkills {
		localNames := make(map[string]bool, len(localSkills)*2)
		for _, skill := range localSkills {
			localNames[strings.ToLower(strings.TrimSpace(skill.Name))] = true
			if norm := subagent.NormalizeSkillName(skill.Name); norm != "" {
				localNames[norm] = true
			}
		}
		for _, skill := range activeSkills {
			// Private instructions keep precedence when the inherited catalog
			// later acquires a skill with the same name, including on restore.
			if localNames[strings.ToLower(skill.Name)] {
				continue
			}
			referenceSkills = append(referenceSkills, skill)
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

	ma = newMultiAgentWithSkills(
		name,
		systemPrompt,
		parent,
		mam.BaseAgent,
		referenceSkills,
		normalizedLocalSkills,
		inheritAllSkills,
	)
	ma.Manager = mam
	ma.supervisor = supervisor

	if !ephemeral {
		dir, err := resolveAgentsDir(agentsDir)
		if err != nil {
			return err
		}
		store, err := subagent.NewStorage(dir)
		if err != nil {
			return err
		}
		ma.stateStore = store
	}

	if err := mam.LoadAgentState(ma); err != nil {
		return fmt.Errorf("load saved state for agent '%s': %w", name, err)
	}
	if persistDef {
		if err := mam.saveAgentDef(ma); err != nil {
			return fmt.Errorf("persist agent '%s': %w", name, err)
		}
		persistedDef = ma.stateStore != nil
	}

	mam.mu.Lock()
	if err := mam.checkMutationContextLocked(ctx); err != nil {
		delete(mam.pending, name)
		pendingReserved = false
		mam.mu.Unlock()
		return err
	}
	if mam.generation != gen {
		delete(mam.pending, name)
		pendingReserved = false
		mam.mu.Unlock()
		return fmt.Errorf("session changed while spawning agent '%s'", name)
	}
	if _, exists := mam.Agents[name]; exists {
		delete(mam.pending, name)
		pendingReserved = false
		mam.mu.Unlock()
		return fmt.Errorf("agent '%s' already exists", name)
	}
	if parentName != "" {
		currentParent, exists := mam.Agents[parentName]
		if !exists || currentParent != parent {
			delete(mam.pending, name)
			pendingReserved = false
			mam.mu.Unlock()
			return fmt.Errorf("parent agent '%s' is no longer active", parentName)
		}
		if err := subagent.MaxDepthAllowed(mam.BaseAgent.LiveSettings().MaxSubagentDepth, parent.Depth()+1); err != nil {
			delete(mam.pending, name)
			pendingReserved = false
			mam.mu.Unlock()
			return fmt.Errorf("subagent nesting disallowed: %w", err)
		}
	}

	mam.Agents[name] = ma
	if parent != nil {
		parent.SubagentsMu.Lock()
		parent.Subagents[name] = ma
		parent.SubagentsMu.Unlock()
	}
	if !mam.staging {
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		if mam.BaseAgent.SpawnedAgents == nil {
			mam.BaseAgent.SpawnedAgents = make(map[string]bool)
		}
		mam.BaseAgent.SpawnedAgents[name] = true
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
		mam.BaseAgent.Registry.Register(subagentExecutor(ma))
		ma.Start(mam.w, mam.theme)
	}
	delete(mam.pending, name)
	pendingReserved = false
	registered = true
	mam.mu.Unlock()
	return nil
}

func subagentExecutor(ma *MultiAgent) *SubagentExecutor {
	name := ma.Name
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
					"prompt":          tool.StringProp(tpl.SubagentParamPrompt + " Send a focused task; avoid forwarding conversation history. Total handoff limit: 16 KiB."),
					"constraints":     {Type: "array", Description: "User and workspace constraints the child must preserve, including offline or validation restrictions (up to 16).", Items: &tool.SchemaProp{Type: "string"}},
					"relevant_paths":  {Type: "array", Description: "Workspace files or directories relevant to this task (up to 16); the child reads them as needed.", Items: &tool.SchemaProp{Type: "string"}},
					"output_contract": tool.StringProp("Optional expected result format and acceptance criteria; default is brief findings/changes, locations, actual checks, and unresolved issues."),
				},
				Required: []string{"prompt"},
			},
		},
	}
	return &SubagentExecutor{
		subagent: ma,
		def:      toolDef,
	}
}

func (mam *MultiAgentManager) resolveAgentLocked(name string) (string, *MultiAgent, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", nil, false
	}
	if ma, exists := mam.Agents[trimmed]; exists {
		return trimmed, ma, true
	}
	norm := subagent.NormalizeAgentName(trimmed)
	if norm == "" {
		return "", nil, false
	}
	if ma, exists := mam.Agents[norm]; exists {
		return norm, ma, true
	}
	for existingName, ma := range mam.Agents {
		if subagent.NormalizeAgentName(existingName) == norm {
			return existingName, ma, true
		}
	}
	return "", nil, false
}

func (mam *MultiAgentManager) ResolveAgentName(name string) (string, bool) {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	resolved, _, exists := mam.resolveAgentLocked(name)
	return resolved, exists
}

func (mam *MultiAgentManager) SendMessage(name string, content string) error {
	mam.mu.RLock()
	resolvedName, ma, exists := mam.resolveAgentLocked(name)
	mam.mu.RUnlock()
	if !exists {
		return fmt.Errorf("agent '%s' not found", name)
	}
	name = resolvedName
	if ma.Context.Err() != nil {
		return fmt.Errorf("agent '%s' is no longer running", name)
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
	return mam.removeAgentFor(nil, name)
}

func (mam *MultiAgentManager) removeAgentFor(ctx tool.AgentContext, name string) error {
	mam.mu.Lock()
	if err := mam.checkMutationContextLocked(ctx); err != nil {
		mam.mu.Unlock()
		return err
	}

	resolvedName, ma, exists := mam.resolveAgentLocked(name)
	if !exists {
		mam.mu.Unlock()
		return fmt.Errorf("agent '%s' not found", name)
	}
	name = resolvedName
	if origin, ok := ctx.(*multiAgentContext); ok && origin.ma == ma {
		mam.mu.Unlock()
		return fmt.Errorf("subagent '%s' cannot remove itself", name)
	}
	if mam.pending[name] {
		mam.mu.Unlock()
		return fmt.Errorf("agent '%s' is already being modified", name)
	}

	parent := ma.parent()
	parentName := ""
	if parent != nil {
		parentName = parent.Name
		if mam.pending[parentName] {
			mam.mu.Unlock()
			return fmt.Errorf("parent agent '%s' is currently being modified", parentName)
		}
	}

	ma.SubagentsMu.RLock()
	childNames := make([]string, 0, len(ma.Subagents))
	for childName := range ma.Subagents {
		childNames = append(childNames, childName)
	}
	sort.Strings(childNames)
	children := make([]*MultiAgent, 0, len(childNames))
	for _, childName := range childNames {
		if mam.pending[childName] {
			ma.SubagentsMu.RUnlock()
			mam.mu.Unlock()
			return fmt.Errorf("child agent '%s' is currently being modified", childName)
		}
		children = append(children, ma.Subagents[childName])
	}
	ma.SubagentsMu.RUnlock()

	if mam.pending == nil {
		mam.pending = make(map[string]bool)
	}
	reserved := make([]string, 0, 1+len(childNames))
	mam.pending[name] = true
	reserved = append(reserved, name)
	for _, childName := range childNames {
		mam.pending[childName] = true
		reserved = append(reserved, childName)
	}
	gen := mam.generation
	mam.mu.Unlock()

	// Persist reparenting and deletion outside mam.mu so concurrent readers and
	// unrelated subagents are never stalled on filesystem syncs.
	persistErr := mam.persistAgentRemovalSnapshot(ma, parentName, children)

	mam.mu.Lock()
	clearReserved := func() {
		for _, rName := range reserved {
			delete(mam.pending, rName)
		}
	}
	if persistErr != nil {
		clearReserved()
		mam.mu.Unlock()
		return fmt.Errorf("remove saved subagent %q: %w", name, persistErr)
	}
	if mam.generation != gen || mam.closed || mam.clearing || mam.Agents[name] != ma {
		clearReserved()
		mam.mu.Unlock()
		return fmt.Errorf("session changed while removing agent '%s'", name)
	}

	delete(mam.Agents, name)

	if mam.BaseAgent != nil {
		mam.BaseAgent.SpawnedAgentsMu.Lock()
		delete(mam.BaseAgent.SpawnedAgents, name)
		mam.BaseAgent.SpawnedAgentsMu.Unlock()
	}

	parent = ma.parent()
	if parent != nil {
		parent.SubagentsMu.Lock()
		delete(parent.Subagents, name)
		parent.SubagentsMu.Unlock()
	}

	ma.SubagentsMu.Lock()
	for childName, child := range ma.Subagents {
		if child != nil {
			child.setParent(parent)
			if parent != nil {
				parent.SubagentsMu.Lock()
				parent.Subagents[childName] = child
				parent.SubagentsMu.Unlock()
			}
		}
	}
	ma.Subagents = make(map[string]*MultiAgent)
	ma.SubagentsMu.Unlock()

	if mam.BaseAgent != nil && mam.BaseAgent.Registry != nil {
		mam.BaseAgent.Registry.Unregister(fmt.Sprintf("subagent__%s", name))
	}

	if mam.ActiveAgent == ma {
		mam.ActiveAgent = parent
	}
	clearReserved()
	mam.mu.Unlock()
	ma.stop()
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
	history := message.CloneAll(active.History)
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
	_, _, exists := mam.resolveAgentLocked(name)
	return exists
}

func (mam *MultiAgentManager) GetParentName(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	_, ag, exists := mam.resolveAgentLocked(name)
	if !exists || ag.parent() == nil {
		return ""
	}
	return ag.parent().Name
}

func (mam *MultiAgentManager) GetAgentSystemPrompt(name string) string {
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	_, ag, exists := mam.resolveAgentLocked(name)
	if !exists {
		return ""
	}
	return ag.SystemPrompt
}

func (mam *MultiAgentManager) JoinAgent(name string) bool {
	mam.mu.Lock()
	defer mam.mu.Unlock()

	trimmed := strings.TrimSpace(name)
	if strings.EqualFold(trimmed, "base") || strings.EqualFold(trimmed, "main") || trimmed == "" {
		mam.ActiveAgent = nil
		return true
	}
	if _, ag, exists := mam.resolveAgentLocked(trimmed); exists {
		mam.ActiveAgent = ag
		return true
	}
	return false
}
