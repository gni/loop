package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
)

type AuditSubagentTool struct {
	mam *MultiAgentManager
}

func (s *AuditSubagentTool) Name() string { return "inspect_subagent" }
func (s *AuditSubagentTool) PromptSnippet() string {
	return tool.FormatToolSnippet("inspect_subagent", "Review a subagent's action, thought, and tool history")
}
func (s *AuditSubagentTool) Definition() tool.Tool {
	return singleNameToolDefinition(
		"inspect_subagent",
		tool.FormatToolDescription("inspect_subagent", "Inspect a subagent execution history, including actions, tool calls, reasoning, and results. Reviewing history does not independently verify correctness."),
		tool.FormatParamDescription("inspect_subagent", "name", "The name of the subagent to inspect."),
	)
}

func (s *AuditSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}

	s.mam.mu.RLock()
	child, exists := s.mam.Agents[name]
	s.mam.mu.RUnlock()

	if !exists {
		s.mam.mu.RLock()
		agentsDir, err := s.mam.getAgentsDir()
		ephemeral := s.mam.ephemeral
		s.mam.mu.RUnlock()
		if err != nil || ephemeral {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}
		path := filepath.Join(agentsDir, name+"_state.json")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}

		store, err := subagent.NewStorage(agentsDir)
		if err != nil {
			return "", err
		}
		child = &MultiAgent{Name: name, stateStore: store}
		if err := s.mam.LoadAgentState(child); err != nil {
			return "", fmt.Errorf("failed to load subagent state: %w", err)
		}
	}

	child.HistoryMu.RLock()
	history := message.CloneAll(child.History)
	child.HistoryMu.RUnlock()

	if len(history) == 0 {
		return tool.RuntimeMessagef("audit_no_history", "No execution history found for subagent '%s'.", name), nil
	}

	// Config (limits.audit_truncation_chars) is authoritative; the prompt catalog
	// value remains a compatibility override.
	limit := limits.AuditTruncationChars()
	if v := tool.RuntimeMessage("audit_truncation_limit", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	var sb strings.Builder
	sb.WriteString(tool.RuntimeMessagef("audit_header", "=== Swarm Audit Trail for Subagent: '%s' ===\n\n", name))

	step := 1
	for _, msg := range history {
		if msg.Role == "system" {
			if strings.HasPrefix(msg.Content, "loaded reference skill") {
				sb.WriteString(tool.RuntimeMessagef("audit_system_line", "[System] %s\n\n", msg.Content))
			}
			continue
		}

		if msg.Role == "user" {
			sb.WriteString(tool.RuntimeMessagef("audit_step_line", "Step %d: [Task Assigned from %s]\n", step, msg.Name))
			sb.WriteString(tool.RuntimeMessagef("audit_prompt_line", "Prompt: %s\n\n", msg.Content))
			step++
			continue
		}

		if msg.Role == "assistant" {
			if msg.ReasoningContent != "" {
				sb.WriteString(tool.RuntimeMessagef("audit_thought_line", "Thought:\n%s\n\n", msg.ReasoningContent))
			}
			if msg.Content != "" {
				sb.WriteString(tool.RuntimeMessagef("audit_response_line", "Response:\n%s\n\n", msg.Content))
			}
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					sb.WriteString(tool.RuntimeMessagef("audit_action_line", "Action (Tool Call): %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
			continue
		}

		if msg.Role == "tool" {
			out := msg.Content
			if len(out) > limit {
				out = out[:limit] + tool.RuntimeMessage("audit_truncation_notice", "\n... (output truncated for audit readability) ...")
			}
			sb.WriteString(tool.RuntimeMessagef("audit_result_line", "Result (Tool Output - %s):\n%s\n\n", msg.Name, out))
			continue
		}
	}

	return sb.String(), nil
}
