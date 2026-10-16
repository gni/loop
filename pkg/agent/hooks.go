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

	cmd := exec.Command("bash", "-c", a.Config.BeforeToolHook)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = os.Environ()

	payload := map[string]string{
		"tool_call_id": tc.ID,
		"name":         tc.Function.Name,
		"arguments":    tc.Function.Arguments,
	}
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = strings.TrimSpace(stdout.String())
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

	cmd := exec.Command("bash", "-c", a.Config.AfterToolHook)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = os.Environ()

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
	payloadBytes, _ := json.Marshal(payload)

	cmd.Stdin = bytes.NewReader(payloadBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	if hookOutput := stdout.String(); hookOutput != "" {
		return hookOutput, nil
	}
	return output, toolErr
}