package swarm

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type taskRequest struct {
	ctx  context.Context
	done chan struct{}
}

type MultiAgent struct {
	planMu        sync.RWMutex
	todos         []tool.TodoItem
	Name          string
	SystemPrompt  string
	History       []db.Message
	HistoryMu     sync.RWMutex
	Skills        []tool.Skill
	LocalSkills   []tool.Skill
	HasAllSkills  bool
	Input         chan db.Message
	Output        chan db.Message
	Subagents     map[string]*MultiAgent
	SubagentsMu   sync.RWMutex
	BaseAgent     *agent.Agent
	Parent        *MultiAgent
	parentMu      sync.RWMutex
	Manager       *MultiAgentManager
	Context       context.Context
	Cancel        context.CancelFunc
	stateStore    *subagent.Storage
	supervisor    *subagent.TaskSupervisor
	persistenceMu sync.Mutex
	started       bool
	stopped       bool
	loopWG        sync.WaitGroup

	ActiveContext context.Context
	ActiveCancel  context.CancelFunc
	ActiveStarted time.Time
	requests      map[string]taskRequest

	StreamBuffer   *bytes.Buffer
	StreamBufferMu sync.Mutex

	BashTool tool.ToolExecutor
	TaskIDs  []string
	TasksMu  sync.Mutex
}

func (ma *MultiAgent) RecordTask(id string) {
	if ma == nil || id == "" {
		return
	}
	ma.TasksMu.Lock()
	if ma.Context != nil && ma.Context.Err() != nil {
		ma.TasksMu.Unlock()
		if ma.BaseAgent != nil {
			_ = ma.BaseAgent.KillTask(id)
		}
		return
	}
	ma.TaskIDs = append(ma.TaskIDs, id)
	ma.TasksMu.Unlock()
}

func (ma *MultiAgent) KillTasks() {
	if ma == nil || ma.BaseAgent == nil {
		return
	}
	ma.TasksMu.Lock()
	ids := append([]string(nil), ma.TaskIDs...)
	ma.TaskIDs = nil
	ma.TasksMu.Unlock()
	for _, id := range ids {
		_ = ma.BaseAgent.KillTask(id)
	}
}

func (ma *MultiAgent) GetSystemPrompt() string {
	var activeAgents []string
	if ma != nil {
		ma.SubagentsMu.RLock()
		for name := range ma.Subagents {
			activeAgents = append(activeAgents, name)
		}
		ma.SubagentsMu.RUnlock()
		sort.Strings(activeAgents)
	}

	allowlist := ma.GetToolAllowlist()
	canSpawn := slices.ContainsFunc(allowlist, tool.IsSpawnTool)

	cfg := ma.BaseAgent.BaseSystemPromptConfig(allowlist, canSpawn)
	cfg.BaseInstruction = ma.SystemPrompt
	if canSpawn {
		cfg.ActiveAgents = activeAgents
	} else {
		cfg.ActiveAgents = nil
	}
	cfg.AllSkills = cfg.Skills
	cfg.Skills = ma.GetEffectiveSkills()
	ma.HistoryMu.RLock()
	if ma.HasAllSkills {
		cfg.LoadedSkills = append([]tool.Skill(nil), ma.LocalSkills...)
	} else {
		cfg.LoadedSkills = append([]tool.Skill(nil), ma.Skills...)
	}
	ma.HistoryMu.RUnlock()
	cfg.GuidedMode = false
	cfg.ActiveTasks = nil

	return agent.BuildSystemPrompt(cfg)
}

func newMultiAgentWithSkills(
	name string,
	systemPrompt string,
	parent *MultiAgent,
	baseAgent *agent.Agent,
	referenceSkills []tool.Skill,
	localSkills []tool.Skill,
	inheritAllSkills bool,
) *MultiAgent {
	parentCtx := context.Background()
	if baseAgent != nil {
		if baseAgent.Lifetime != nil {
			parentCtx = baseAgent.Lifetime
		} else {
			parentCtx = baseAgent.Context()
		}
	}
	ctx, cancel := context.WithCancel(parentCtx)
	combined := make([]tool.Skill, 0, len(referenceSkills)+len(localSkills))
	combined = append(combined, referenceSkills...)
	combined = append(combined, localSkills...)

	ma := &MultiAgent{
		Name:         name,
		SystemPrompt: systemPrompt,
		Parent:       parent,
		BaseAgent:    baseAgent,
		Skills:       combined,
		LocalSkills:  localSkills,
		HasAllSkills: inheritAllSkills,
		Input:        make(chan db.Message, 100),
		Output:       make(chan db.Message, 100),
		Subagents:    make(map[string]*MultiAgent),
		Context:      ctx,
		Cancel:       cancel,
		BashTool:     tool.NewBashTool(),
	}

	sysPrompt := ma.GetSystemPrompt()
	ma.HistoryMu.Lock()
	ma.History = []db.Message{
		{Role: "system", Content: sysPrompt},
	}
	ma.HistoryMu.Unlock()

	return ma
}

func (ma *MultiAgent) GetEffectiveSkills() []tool.Skill {
	ma.HistoryMu.RLock()
	defer ma.HistoryMu.RUnlock()
	return append([]tool.Skill(nil), ma.Skills...)
}

// parent snapshots the hierarchy independently of the manager registry lock.
// Running agents may inspect depth while the manager reparents their branch.
func (ma *MultiAgent) parent() *MultiAgent {
	ma.parentMu.RLock()
	defer ma.parentMu.RUnlock()
	return ma.Parent
}

func (ma *MultiAgent) setParent(parent *MultiAgent) {
	ma.parentMu.Lock()
	ma.Parent = parent
	ma.parentMu.Unlock()
}

func (ma *MultiAgent) Depth() int {
	depth := 0
	for curr := ma.parent(); curr != nil; curr = curr.parent() {
		depth++
	}
	return depth
}

func (ma *MultiAgent) GetToolAllowlist() []string {
	if ma.BaseAgent == nil || ma.BaseAgent.Registry == nil {
		return nil
	}

	maxDepth := ma.BaseAgent.LiveSettings().MaxSubagentDepth
	canSpawn := ma.Depth()+1 < subagent.EffectiveMaxDepth(maxDepth)

	ma.BaseAgent.LLMProviderMu.RLock()
	selection := append([]string(nil), ma.BaseAgent.ToolSelection...)
	ma.BaseAgent.LLMProviderMu.RUnlock()
	var selectedSet map[string]bool
	if len(selection) > 0 {
		selectedSet = make(map[string]bool, len(selection))
		for _, name := range selection {
			selectedSet[name] = true
		}
	}

	allTools := ma.BaseAgent.Registry.GetAllExecutors()
	allowlist := make([]string, 0, len(allTools))
	for name := range allTools {
		if ma.BaseAgent.Registry.IsDisabled(name) {
			continue
		}
		if after, ok := strings.CutPrefix(name, "subagent__"); ok {
			if selectedSet != nil && !selectedSet[name] && !selectedSet["spawn_subagent"] {
				continue
			}
			targetName := after
			ma.SubagentsMu.RLock()
			_, isChild := ma.Subagents[targetName]
			ma.SubagentsMu.RUnlock()
			if isChild {
				allowlist = append(allowlist, name)
			}
		} else if selectedSet != nil && !selectedSet[name] {
			continue
		} else if tool.IsSubagentTool(name) {
			if canSpawn {
				allowlist = append(allowlist, name)
			}
		} else {
			allowlist = append(allowlist, name)
		}
	}

	sort.Strings(allowlist)
	return allowlist
}
