package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/resources"
)

func (s *Session) transformInput(ctx context.Context, prompt string) (string, error) {
	for _, hook := range s.resourceHooks() {
		if hook.Event != "input" {
			continue
		}
		result, err := resources.RunHook(ctx, hook, map[string]any{"version": 1, "type": "input", "prompt": prompt, "session_id": s.ID})
		if err != nil {
			return "", err
		}
		if result.Block {
			return "", fmt.Errorf("input blocked: %s", result.Reason)
		}
		if result.Prompt != nil {
			prompt = *result.Prompt
		}
	}
	return prompt, nil
}

func (s *Session) transformContext(ctx context.Context, messages []db.Message) ([]db.Message, error) {
	for _, hook := range s.resourceHooks() {
		if hook.Event != "context" {
			continue
		}
		result, err := resources.RunHook(ctx, hook, map[string]any{"version": 1, "type": "context", "messages": messages, "session_id": s.ID})
		if err != nil {
			return nil, err
		}
		if result.Block {
			return nil, fmt.Errorf("context blocked: %s", result.Reason)
		}
		if len(result.Messages) > 0 {
			if strings.TrimSpace(string(result.Messages)) == "null" {
				return nil, fmt.Errorf("extension context messages must be an array")
			}
			if err := json.Unmarshal(result.Messages, &messages); err != nil {
				return nil, fmt.Errorf("extension context: %w", err)
			}
			for _, msg := range messages {
				switch msg.Role {
				case "system", "user", "assistant", "tool":
				default:
					return nil, fmt.Errorf("extension returned invalid message role %q", msg.Role)
				}
			}
		}
	}
	return agent.EnforceToolPairingInvariance(messages), nil
}

func (s *Session) permissionHook(event agent.ToolCallEvent) *agent.PermissionDecision {
	for _, hook := range s.resourceHooks() {
		if event.Type != hook.Event {
			continue
		}
		response, err := resources.RunHook(s.Agent.Context(), hook, map[string]any{"version": 1, "type": event.Type, "tool_call": event.ToolCall, "output": event.Output, "is_error": event.Err != nil})
		if err != nil {
			return &agent.PermissionDecision{Allowed: false, Reason: err.Error()}
		}
		if response.Block {
			return &agent.PermissionDecision{Allowed: false, Reason: response.Reason}
		}
	}
	return nil
}

func (s *Session) resourceHooks() []resources.Hook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]resources.Hook(nil), s.hooks...)
}
