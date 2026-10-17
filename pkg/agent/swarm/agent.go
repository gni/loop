package swarm

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type MultiAgent struct {
	Name         string
	SystemPrompt string
	History      []db.Message
	HistoryMu    sync.RWMutex
	Skills       []tool.Skill
	LocalSkills  []tool.Skill
	HasAllSkills bool
	Input        chan db.Message
	Output       chan db.Message
	Subagents    map[string]*MultiAgent
	SubagentsMu  sync.RWMutex
	BaseAgent    *agent.Agent
	Parent       *MultiAgent
	Manager      *MultiAgentManager
	Context      context.Context
	Cancel       context.CancelFunc

	ActiveContext context.Context
	ActiveCancel  context.CancelFunc
	ActiveStarted time.Time

	StreamBuffer   *bytes.Buffer
	StreamBufferMu sync.Mutex
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

	workspaceRoot := "."
	skillsDir := "skills"
	compact := false
	guidedMode := false
	memoryContext := ""
	var allSkills []tool.Skill
	if ma.BaseAgent != nil {
		workspaceRoot = ma.BaseAgent.WorkspaceRoot
		guidedMode = ma.BaseAgent.IsGuidedMode()
		memoryContext = ma.BaseAgent.LoadMemoryContext()
		if ma.BaseAgent.Config != nil {
			skillsDir = ma.BaseAgent.Config.SkillsDir
			compact = ma.BaseAgent.Config.CompactPrompt
		}
		allSkills = ma.BaseAgent.GetActiveSkills()
	}

	allowlist := ma.GetToolAllowlist()
	canSpawn := false
	for _, toolName := range allowlist {
		if tool.IsSpawnTool(toolName) {
			canSpawn = true
			break
		}
	}

	var tools []agent.ToolEntry
	if ma.BaseAgent != nil {
		tools = agent.PromptToolEntries(ma.BaseAgent.Registry, canSpawn)
	}

	return agent.BuildSystemPrompt(agent.SystemPromptConfig{
		BaseInstruction: ma.SystemPrompt,
		WorkspaceRoot:   workspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		GuidedMode:      guidedMode,
		ActiveAgents:    activeAgents,
		Skills:          ma.GetEffectiveSkills(),
		AllSkills:       allSkills,
		MemoryContext:   memoryContext,
		Allowlist:       allowlist,
		Tools:           tools,
	})
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
		parentCtx = baseAgent.Context()
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

func (ma *MultiAgent) Depth() int {
	depth := 0
	for curr := ma.Parent; curr != nil; curr = curr.Parent {
		depth++
	}
	return depth
}

func (ma *MultiAgent) GetToolAllowlist() []string {
	if ma.BaseAgent == nil || ma.BaseAgent.Registry == nil {
		return nil
	}

	maxDepth := 0
	if ma.BaseAgent.Config != nil {
		maxDepth = ma.BaseAgent.Config.MaxSubagentDepth
	}
	canSpawn := ma.Depth() < subagent.EffectiveMaxDepth(maxDepth)

	allTools := ma.BaseAgent.Registry.GetAllExecutors()
	allowlist := make([]string, 0, len(allTools))
	for name := range allTools {
		if ma.BaseAgent.Registry.IsDisabled(name) {
			continue
		}
		if strings.HasPrefix(name, "subagent__") {
			targetName := strings.TrimPrefix(name, "subagent__")
			ma.SubagentsMu.RLock()
			_, isChild := ma.Subagents[targetName]
			ma.SubagentsMu.RUnlock()
			if isChild {
				allowlist = append(allowlist, name)
			}
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