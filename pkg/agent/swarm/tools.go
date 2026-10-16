package swarm

import (
	"fmt"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type SubagentExecutor struct {
	subagent *MultiAgent
	def      tool.Tool
}

type subagentExecutor = SubagentExecutor

func (s *SubagentExecutor) Name() string          { return s.def.Function.Name }
func (s *SubagentExecutor) Definition() tool.Tool { return s.def }

func drainSubagentOutput(ch <-chan db.Message) {
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (s *SubagentExecutor) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	var raw string
	var args struct {
		Prompt  string `json:"prompt"`
		Task    string `json:"task"`
		Input   string `json:"input"`
		Message string `json:"message"`
	}
	if err := tool.ParseToolArguments(arguments, &args, func(v string) { raw = v }); err != nil {
		raw = arguments
	}
	prompt := strings.TrimSpace(tool.FirstNonEmpty(raw, args.Prompt, args.Task, args.Input, args.Message))
	if prompt == "" {
		return "", fmt.Errorf("missing required argument: prompt")
	}

	if s.subagent.Manager == nil {
		return "", fmt.Errorf("subagent '%s' has no manager reference", s.subagent.Name)
	}

	drainSubagentOutput(s.subagent.Output)

	taskID := fmt.Sprintf("subtask_%s", db.NewUUID()[:8])
	s.subagent.Manager.RegisterTask(taskID, s.subagent.Name, prompt)

	select {
	case <-ctx.Context().Done():
		s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
		return "", ctx.Context().Err()
	case <-s.subagent.Context.Done():
		errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
		s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errSub)
		return "", errSub
	case s.subagent.Input <- db.Message{
		Role:       "user",
		Name:       "ParentAgent",
		ToolCallID: taskID,
		Content:    prompt,
	}:
	}

	const timeout = 60 * time.Minute
	const maxResponseRunes = 10000

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	checkTask := func() (string, bool, error) {
		task, err := s.subagent.Manager.GetTask(taskID)
		if err != nil {
			return "", true, err
		}
		switch task.Status {
		case "completed":
			drainSubagentOutput(s.subagent.Output)
			runes := []rune(task.Response)
			if len(runes) > maxResponseRunes {
				return string(runes[:maxResponseRunes]) + fmt.Sprintf("\n\n... [Response truncated: subagent returned %d characters. To prevent context overflow, output is capped at %d characters. If you need the full detailed report, please instruct the subagent to write its response directly to a file on disk.]", len(runes), maxResponseRunes), true, nil
			}
			return task.Response, true, nil
		case "failed":
			drainSubagentOutput(s.subagent.Output)
			return "", true, fmt.Errorf("subagent execution failed: %s", task.Error)
		default:
			return "", false, nil
		}
	}

	for {
		select {
		case <-ctx.Context().Done():
			s.subagent.CancelActiveTurn()
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
			drainSubagentOutput(s.subagent.Output)
			return "", ctx.Context().Err()
		case <-s.subagent.Context.Done():
			errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errSub)
			return "", errSub
		case <-timeoutTimer.C:
			s.subagent.CancelActiveTurn()
			errTimeout := fmt.Errorf("subagent '%s' execution timed out after %v", s.subagent.Name, timeout)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errTimeout)
			drainSubagentOutput(s.subagent.Output)
			return "", errTimeout
		case <-s.subagent.Output:
			if res, done, err := checkTask(); done {
				return res, err
			}
		case <-ticker.C:
			if res, done, err := checkTask(); done {
				return res, err
			}
		}
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
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}
	if err := s.mam.RemoveAgent(name); err != nil {
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
		skills, _ := s.mam.ListAgentSkills(name)
		skillStr := "None"
		if len(skills) > 0 {
			skillNames := make([]string, len(skills))
			for i, sk := range skills {
				skillNames[i] = sk.Name
			}
			skillStr = strings.Join(skillNames, ", ")
		}

		sb.WriteString(fmt.Sprintf("- %s (Parent: %s) [Skills: %s]\n", name, parentStr, skillStr))
		sysPrompt := strings.ReplaceAll(s.mam.GetAgentSystemPrompt(name), "\n", " ")
		if len(sysPrompt) > 100 {
			sysPrompt = sysPrompt[:97] + "..."
		}
		sb.WriteString(fmt.Sprintf("  Goal: %s\n", sysPrompt))
	}
	return sb.String(), nil
}