package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"loop/pkg/process"
	"os"
	"strings"
	"time"

	"loop/pkg/db"
)

var PermissionHook func(ToolCallEvent) *PermissionDecision

func (a *Agent) executeHookScript(script string, payload any) (string, string, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	timeout := 15 * time.Second
	if a.Config != nil && a.Config.ExtensionTimeout > 0 {
		timeout = time.Duration(a.Config.ExtensionTimeout) * time.Second
	}
	result, err := process.Run(a.Context(), process.Options{Executable: "bash", Arguments: []string{"-c", script}, Directory: a.WorkspaceRoot, Environment: os.Environ(), Input: bytes.NewReader(payloadBytes), Timeout: timeout})
	return result.Stdout, result.Stderr, err

}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	if a.Bus == nil && PermissionHook != nil {
		if d := PermissionHook(ToolCallEvent{Type: "tool_call", ToolCall: tc}); d != nil && !d.Allowed {
			return false, d.Reason
		}
	}
	bus := a.Bus
	if bus == nil {
		bus = EventBusInstance()
	}
	decision := bus.Emit(ToolCallEvent{Type: "tool_call", ToolCall: tc})
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
	bus := a.Bus
	if bus == nil {
		bus = EventBusInstance()
	}
	if decision := bus.Emit(ToolCallEvent{Type: "tool_result", ToolCall: tc, Output: output, Err: toolErr}); decision != nil && !decision.Allowed {
		return output, errors.Join(toolErr, fmt.Errorf("result hook blocked: %s", decision.Reason))
	}
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
		return stdout, toolErr
	}
	return output, toolErr
}
