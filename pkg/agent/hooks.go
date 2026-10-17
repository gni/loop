package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"loop/pkg/db"
)

var PermissionHook func(ToolCallEvent) *PermissionDecision

func (a *Agent) executeHookScript(script string, payload any) (string, string, error) {
	cmd := exec.CommandContext(a.Context(), "bash", "-c", script)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = os.Environ()

	payloadBytes, _ := json.Marshal(payload)
	cmd.Stdin = bytes.NewReader(payloadBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	if PermissionHook != nil {
		if d := PermissionHook(ToolCallEvent{Type: "tool_call", ToolCall: tc}); d != nil && !d.Allowed {
			return false, d.Reason
		}
	}
	decision := EventBusInstance().Emit(ToolCallEvent{Type: "tool_call", ToolCall: tc})
	if decision != nil && !decision.Allowed {
		return false, decision.Reason
	}
	return a.runExecBeforeHook(tc)
}

func (a *Agent) runExecBeforeHook(tc db.ToolCall) (bool, string) {
	if a == nil || a.Config == nil || a.Config.BeforeToolHook == "" {
		return true, ""
	}

	payload := map[string]string{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
	}
	stdout, stderr, err := a.executeHookScript(a.Config.BeforeToolHook, payload)
	if err != nil {
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			reason = strings.TrimSpace(stdout)
		}
		if reason == "" {
			reason = err.Error()
		}
		return false, reason
	}
	return true, ""
}

func (a *Agent) runAfterToolHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	EventBusInstance().Emit(ToolCallEvent{Type: "tool_result", ToolCall: tc, Output: output, Err: toolErr})
	return a.runExecAfterHook(tc, output, toolErr)
}

func (a *Agent) runExecAfterHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	if a == nil || a.Config == nil || a.Config.AfterToolHook == "" {
		return output, toolErr
	}

	errStr := ""
	if toolErr != nil {
		errStr = toolErr.Error()
	}

	payload := map[string]interface{}{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
		"output":       output,
		"error":        errStr,
	}
	stdout, stderr, err := a.executeHookScript(a.Config.AfterToolHook, payload)
	if err != nil {
		reason := strings.TrimSpace(stderr)
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	if stdout != "" {
		return stdout, nil
	}
	return output, toolErr
}