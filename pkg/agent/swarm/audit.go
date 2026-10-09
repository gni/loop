package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

type AuditSubagentTool struct {
	mam *MultiAgentManager
}

func (s *AuditSubagentTool) Name() string { return "audit_subagent" }
func (s *AuditSubagentTool) PromptSnippet() string {
	return "Review a subagent's action, thought, and tool history"
}
func (s *AuditSubagentTool) Definition() tool.Tool {
	return singleNameToolDefinition(
		"audit_subagent",
		"Audit the execution history of a spawned subagent to see exactly what actions, tool calls, thoughts, and results it produced. Essential for verifying subagent work.",
		"The name of the subagent to audit (e.g. 'coder', 'researcher').",
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
		return fmt.Sprintf("No execution history found for subagent '%s'.", name), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Swarm Audit Trail for Subagent: '%s' ===\n\n", name))

	step := 1
	for _, msg := range history {
		if msg.Role == "system" {
			if strings.HasPrefix(msg.Content, "loaded reference skill") {
				sb.WriteString(fmt.Sprintf("[System] %s\n\n", msg.Content))
			}
			continue
		}

		if msg.Role == "user" {
			sb.WriteString(fmt.Sprintf("Step %d: [Task Assigned from %s]\n", step, msg.Name))
			sb.WriteString(fmt.Sprintf("Prompt: %s\n\n", msg.Content))
			step++
			continue
		}

		if msg.Role == "assistant" {
			if msg.ReasoningContent != "" {
				sb.WriteString(fmt.Sprintf("Thought:\n%s\n\n", msg.ReasoningContent))
			}
			if msg.Content != "" {
				sb.WriteString(fmt.Sprintf("Response:\n%s\n\n", msg.Content))
			}
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					sb.WriteString(fmt.Sprintf("Action (Tool Call): %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
			continue
		}

		if msg.Role == "tool" {
			out := msg.Content
			if len(out) > 1500 {
				out = out[:1500] + "\n... (output truncated for audit readability) ..."
			}
			sb.WriteString(fmt.Sprintf("Result (Tool Output - %s):\n%s\n\n", msg.Name, out))
			continue
		}
	}

	return sb.String(), nil
}
