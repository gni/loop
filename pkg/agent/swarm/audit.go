package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
)

type AuditSubagentTool struct {
	mam *MultiAgentManager
}

func (s *AuditSubagentTool) Name() string { return "audit_subagent" }
func (s *AuditSubagentTool) PromptSnippet() string {
	return tool.FormatToolSnippet("audit_subagent", "Review a subagent's action, thought, and tool history")
}
func (s *AuditSubagentTool) Definition() tool.Tool {
	return singleNameToolDefinition(
		"audit_subagent",
		tool.FormatToolDescription("audit_subagent", "Audit the execution history of a spawned subagent to see exactly what actions, tool calls, thoughts, and results it produced. Essential for verifying subagent work."),
		tool.FormatParamDescription("audit_subagent", "name", "The name of the subagent to audit."),
	)
}

func (s *AuditSubagentTool) Execute(ctx tool.AgentContext, arguments string) (string, error) {
	name, err := parseSubagentName(arguments)
	if err != nil {
		return "", err
	}

	s.mam.mu.RLock()
	subagent, exists := s.mam.Agents[name]
	s.mam.mu.RUnlock()

	if !exists {
		agentsDir, err := s.mam.getAgentsDir()
		if err != nil {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}
		path := filepath.Join(agentsDir, name+"_state.json")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("subagent '%s' not found", name)
		}

		subagent = &MultiAgent{Name: name}
		if err := s.mam.LoadAgentState(subagent); err != nil {
			return "", fmt.Errorf("failed to load subagent state: %w", err)
		}
	}

	subagent.HistoryMu.RLock()
	history := make([]db.Message, len(subagent.History))
	copy(history, subagent.History)
	subagent.HistoryMu.RUnlock()

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
