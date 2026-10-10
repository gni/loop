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

// MultiAgent represents an independent agent node in a multi-agent swarm.
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

	// Per-agent stream buffer: the tee writer in executeLoop tees this agent's output
	// here. It must not be BaseAgent.CurrentStreamBuffer, which the main loop owns and
	// which pkg/ui/redraw.go reads; sharing it made concurrent subagents overwrite each
	// other's redraw content.
	StreamBuffer   *bytes.Buffer
	StreamBufferMu sync.Mutex
}

// GetSystemPrompt generates the system instructions and reference guides list for the agent.
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
	var allSkills []tool.Skill
	if ma.BaseAgent != nil {
		workspaceRoot = ma.BaseAgent.WorkspaceRoot
		if ma.BaseAgent.Config != nil {
			skillsDir = ma.BaseAgent.Config.SkillsDir
			compact = ma.BaseAgent.Config.CompactPrompt
		}
		allSkills = ma.BaseAgent.ActiveSkills
	}

	canSpawn := false
	for _, toolName := range ma.GetToolAllowlist() {
		if tool.IsSpawnTool(toolName) {
			canSpawn = true
			break
		}
	}

	var tools []agent.ToolEntry
	if ma.BaseAgent != nil && ma.BaseAgent.Registry != nil {
		executors := ma.BaseAgent.Registry.GetAllExecutors()
		for name, executor := range executors {
			if !canSpawn && tool.IsSubagentTool(name) {
				continue
			}
			tools = append(tools, agent.ToolEntry{
				Name:       name,
				Snippet:    tool.GetPromptSnippet(executor),
				Guidelines: tool.GetPromptGuidelines(executor),
			})
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	}

	return agent.BuildSystemPrompt(agent.SystemPromptConfig{
		BaseInstruction: ma.SystemPrompt,
		WorkspaceRoot:   workspaceRoot,
		SkillsDir:       skillsDir,
		CompactPrompt:   compact,
		ActiveAgents:    activeAgents,
		Skills:          ma.GetEffectiveSkills(),
		AllSkills:       allSkills,
		Allowlist:       ma.GetToolAllowlist(),
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
	// Derive from the base agent's active context so the process root context from
	// lifecycle.Default() propagates to subagents; Ctrl+C then cancels the whole tree
	// instead of relying on CancelActiveTurn for the active agent only.
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
	curr := ma.Parent
	for curr != nil {
		depth++
		curr = curr.Parent
	}
	return depth
}

func (ma *MultiAgent) GetToolAllowlist() []string {
	if ma.BaseAgent == nil || ma.BaseAgent.Registry == nil {
		return nil
	}

	depth := ma.Depth()

	maxDepth := 0
	if ma.BaseAgent.Config != nil {
		maxDepth = ma.BaseAgent.Config.MaxSubagentDepth
	}

	canSpawn := depth < subagent.EffectiveMaxDepth(maxDepth)

	allTools := ma.BaseAgent.Registry.GetAllExecutors()
	var allowlist []string
	for name := range allTools {
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

	return allowlist
}
