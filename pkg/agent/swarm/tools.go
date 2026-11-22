package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/run"
	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/task"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

type SubagentExecutor struct {
	mu       sync.Mutex
	subagent *MultiAgent
	def      tool.Tool
	gate     chan struct{}
}

func (s *SubagentExecutor) Name() string          { return s.def.Function.Name }
func (s *SubagentExecutor) Definition() tool.Tool { return s.def }

func drainSubagentOutput(ch <-chan db.Message) {
	if ch == nil {
		return
	}
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (s *SubagentExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	result, err := s.ExecuteResult(ctx, arguments)
	return result.Output, err
}

func (s *SubagentExecutor) ExecuteResult(ctx tool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	var assessment *task.State
	output, err := s.execute(ctx, arguments, &assessment)
	status := evidence.Succeeded
	if err != nil {
		status = evidence.Failed
	}
	verification := ""
	observed := &evidence.Record{Kind: "subagent", Target: s.Name(), Outcome: status}
	if assessment != nil {
		verification = assessment.Verification.Stage
		observed.Checks = evidence.CloneChecks(assessment.Verification.Checks)
		observed.FileRevisions = assessment.Revisions
		observed.MutationAt = assessment.Verification.ChangedAt
		changed := len(assessment.Revisions) > 0
		observed.Changed = &changed
	}
	structured, _ := json.Marshal(struct {
		Agent        string `json:"agent"`
		Status       string `json:"status"`
		Summary      string `json:"summary"`
		Verification string `json:"verification,omitempty"`
	}{s.Name(), status, output, verification})
	return domaintool.ToolResult{Output: output, Structured: structured, IsError: err != nil, Evidence: observed}, err
}

func (s *SubagentExecutor) execute(ctx tool.AgentContext, arguments string, assessment **task.State) (string, error) {
	inherited := run.DelegationInstructions(ctx.Context())
	prompt, err := prepareHandoff(ctx, arguments, inherited)
	if err != nil {
		return "", err
	}

	if s.subagent == nil || s.subagent.Manager == nil {
		return "", fmt.Errorf("subagent has no manager reference")
	}
	managerBase := s.subagent.Manager.BaseAgent
	if managerBase == nil || managerBase.LiveSettings().MaxSubagentDepth <= 0 {
		return "", fmt.Errorf("subagents are disabled")
	}
	settings, err := managerBase.Configuration()
	if err != nil {
		return "", err
	}
	seconds := settings.SubagentTimeout
	if seconds <= 0 {
		seconds = 300
	}
	timeout := time.Duration(seconds) * time.Second
	executionCtx, cancel := context.WithTimeout(ctx.Context(), timeout)
	defer cancel()

	s.mu.Lock()
	if s.gate == nil {
		s.gate = make(chan struct{}, 1)
	}
	gate := s.gate
	s.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-executionCtx.Done():
		return "", executionCtx.Err()
	}
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}

	drainSubagentOutput(s.subagent.Output)

	taskID := "subtask_" + db.NewUUID()
	defer func() {
		if *assessment != nil {
			return
		}
		s.subagent.HistoryMu.RLock()
		defer s.subagent.HistoryMu.RUnlock()
		start := -1
		for i, msg := range s.subagent.History {
			if msg.Role == "user" && msg.ToolCallID == taskID {
				start = i
			}
		}
		if start < 0 {
			return
		}
		end := len(s.subagent.History)
		for i := start + 1; i < end; i++ {
			msg := s.subagent.History[i]
			if msg.IsUserInput() && msg.ToolCallID != taskID {
				end = i
				break
			}
		}
		for i := end - 1; i >= start; i-- {
			if state := s.subagent.History[i].TaskState; state != nil {
				*assessment = task.Clone(state)
				break
			}
		}
	}()
	if err := s.subagent.supervisor.RegisterTask(taskID, s.subagent.Name, prompt); err != nil {
		return "", err
	}
	doneCh := make(chan struct{})
	s.subagent.HistoryMu.Lock()
	if s.subagent.requests == nil {
		s.subagent.requests = make(map[string]taskRequest)
	}
	s.subagent.requests[taskID] = taskRequest{ctx: executionCtx, done: doneCh}
	s.subagent.HistoryMu.Unlock()
	enqueued := false
	defer func() {
		if !enqueued {
			s.subagent.HistoryMu.Lock()
			delete(s.subagent.requests, taskID)
			s.subagent.HistoryMu.Unlock()
		}
	}()

	select {
	case <-executionCtx.Done():
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", executionCtx.Err())
		return "", executionCtx.Err()
	case <-ctx.Context().Done():
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
		return "", ctx.Context().Err()
	case <-s.subagent.Context.Done():
		errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", errSub)
		return "", errSub
	case s.subagent.Input <- db.Message{
		Role:                  "user",
		Name:                  "ParentAgent",
		ToolCallID:            taskID,
		Content:               prompt,
		InheritedInstructions: inherited,
	}:
		enqueued = true
	}

	checkTask := func() (string, bool, error) {
		task, err := s.subagent.supervisor.GetTask(taskID)
		if err != nil {
			return "", true, err
		}
		switch task.Status {
		case "completed":
			*assessment = task.TaskState
			drainSubagentOutput(s.subagent.Output)
			preview, _, _ := tool.SpillToolOutput(ctx, "subagent:"+s.subagent.Name, taskID, s.Name(), task.Response, min(2048, limits.ToolOutputBytes(ctx)))
			return preview, true, nil
		case "failed":
			*assessment = task.TaskState
			drainSubagentOutput(s.subagent.Output)
			return "", true, fmt.Errorf("subagent execution failed: %s", task.Error)
		default:
			return "", false, nil
		}
	}

	select {
	case <-doneCh:
		if res, done, err := checkTask(); done {
			return res, err
		}
		errSub := fmt.Errorf("subagent '%s' finished without recording task status", s.subagent.Name)
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", errSub)
		return "", errSub
	case <-ctx.Context().Done():
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
		drainSubagentOutput(s.subagent.Output)
		return "", ctx.Context().Err()
	case <-s.subagent.Context.Done():
		errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", errSub)
		return "", errSub
	case <-executionCtx.Done():
		errTimeout := fmt.Errorf("subagent '%s' execution stopped after %v: %w", s.subagent.Name, timeout, executionCtx.Err())
		s.subagent.supervisor.UpdateTaskStatus(taskID, "failed", "", errTimeout)
		drainSubagentOutput(s.subagent.Output)
		return "", errTimeout
	}
}

func parseSubagentName(arguments string) (string, error) {
	var raw string
	var args struct {
		Name         string `json:"name"`
		AgentName    string `json:"agent_name"`
		SubagentName string `json:"subagent_name"`
		Agent        string `json:"agent"`
		Subagent     string `json:"subagent"`
	}
	if err := tool.ParseToolArguments(arguments, &args, func(s string) { raw = s }); err != nil {
		return "", err
	}
	name := strings.TrimSpace(tool.FirstNonEmpty(raw, args.Name, args.AgentName, args.SubagentName, args.Agent, args.Subagent))
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	if err := subagent.ValidateAgentName(name); err != nil {
		return "", err
	}
	return name, nil
}

func singleNameToolDefinition(name, description, paramDesc string) tool.Tool {
	return tool.NewFunctionTool(
		name,
		description,
		map[string]tool.SchemaProp{
			"name": tool.StringProp(paramDesc),
		},
		"name",
	)
}

type RemoveSubagentTool struct {
	mam *MultiAgentManager
}

func (s *RemoveSubagentTool) Name() string { return "remove_subagent" }
func (s *RemoveSubagentTool) PromptSnippet() string {
	return tool.FormatToolSnippet(s.Name(), "Terminate a subagent")
}
func (s *RemoveSubagentTool) Definition() tool.Tool {
	return singleNameToolDefinition(
		"remove_subagent",
		tool.FormatToolDescription("remove_subagent", "Terminate a running subagent."),
		tool.FormatParamDescription("remove_subagent", "name", "The name of the subagent to terminate."),
	)
}

func (s *RemoveSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}
	if err := s.mam.removeAgentFor(ctx, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("Subagent '%s' terminated.", name), nil
}

type ListSubagentsTool struct {
	mam *MultiAgentManager
}

func (s *ListSubagentsTool) Name() string { return "list_subagents" }
func (s *ListSubagentsTool) PromptSnippet() string {
	return tool.FormatToolSnippet(s.Name(), "View active subagents")
}
func (s *ListSubagentsTool) Definition() tool.Tool {
	return tool.NewFunctionTool(
		"list_subagents",
		tool.FormatToolDescription("list_subagents", "View active subagents, their parent relationships, and their loaded skills."),
		map[string]tool.SchemaProp{},
	)
}

func (s *ListSubagentsTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	agents := s.mam.ListAgents()
	if len(agents) == 0 {
		return "No subagents currently active.", nil
	}

	var sb strings.Builder
	sb.WriteString("Active Subagents:\n")
	for _, name := range agents {
		parentStr := tool.FirstNonEmpty(s.mam.GetParentName(name), "Base Agent")
		skills, err := s.mam.ListAgentSkills(name)
		if err != nil {
			return "", fmt.Errorf("list skills for subagent %q: %w", name, err)
		}
		skillStr := "None"
		if len(skills) > 0 {
			skillNames := make([]string, len(skills))
			for i, sk := range skills {
				skillNames[i] = sk.Name
			}
			skillStr = strings.Join(skillNames, ", ")
		}

		sb.WriteString(fmt.Sprintf("- %s (Parent: %s) [Skills: %s]\n", name, parentStr, skillStr))
		sysPrompt := style.TruncateRunes(strings.ReplaceAll(s.mam.GetAgentSystemPrompt(name), "\n", " "), 100)
		sb.WriteString(fmt.Sprintf("  Goal: %s\n", sysPrompt))
	}
	return sb.String(), nil
}
