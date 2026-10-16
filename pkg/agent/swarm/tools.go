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

	timeout := 60 * time.Minute
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeoutChan := time.After(timeout)

	for {
		select {
		case <-ctx.Context().Done():
			s.subagent.CancelActiveTurn()
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", ctx.Context().Err())
			return "", ctx.Context().Err()
		case <-s.subagent.Context.Done():
			errSub := fmt.Errorf("subagent '%s' context cancelled", s.subagent.Name)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errSub)
			return "", errSub
		case <-timeoutChan:
			s.subagent.CancelActiveTurn()
			errTimeout := fmt.Errorf("subagent '%s' execution timed out after %v", s.subagent.Name, timeout)
			s.subagent.Manager.UpdateTaskStatus(taskID, "failed", "", errTimeout)
			return "", errTimeout
		case <-ticker.C:
			task, err := s.subagent.Manager.GetTask(taskID)
			if err != nil {
				return "", err
			}
			if task.Status == "completed" {
				if runeCount := len([]rune(task.Response)); runeCount > 10000 {
					return string([]rune(task.Response)[:10000]) + fmt.Sprintf("\n\n... [Response truncated: subagent returned %d characters. To prevent context overflow, output is capped at 10000 characters. If you need the full detailed report, please instruct the subagent to write its response directly to a file on disk.]", runeCount), nil
				}
				return task.Response, nil
			}
			if task.Status == "failed" {
				return "", fmt.Errorf("subagent execution failed: %s", task.Error)
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

type RemoveSubagentTool struct {
	mam *MultiAgentManager
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
		parent := s.mam.GetParentName(name)
		skills, _ := s.mam.ListAgentSkills(name)
		skillNames := make([]string, 0, len(skills))
		for _, sk := range skills {
			skillNames = append(skillNames, sk.Name)
		}
		skillStr := "None"
		if len(skillNames) > 0 {
			skillStr = strings.Join(skillNames, ", ")
		}
		parentStr := "Base Agent"
		if parent != "" {
			parentStr = parent
		}
		sb.WriteString(fmt.Sprintf("- %s (Parent: %s) [Skills: %s]\n", name, parentStr, skillStr))
		sysPrompt := s.mam.GetAgentSystemPrompt(name)
		if len(sysPrompt) > 100 {
			sysPrompt = sysPrompt[:97] + "..."
		}
		sysPrompt = strings.ReplaceAll(sysPrompt, "\n", " ")
		sb.WriteString(fmt.Sprintf("  Goal: %s\n", sysPrompt))
	}
	return sb.String(), nil
}