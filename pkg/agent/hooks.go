package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"loop/pkg/db"
)

var (
	hookRegistered   = map[*Agent]bool{}
	hookRegisteredMu sync.Mutex
)

// PermissionHook is the typed permission-check hook.
// It runs as a bus subscriber before tool execution; default is allow.
var PermissionHook func(ToolCallEvent) *PermissionDecision

// execHookSubscriber registers the shell exec hooks as one EventBus subscriber,
// so typed subscribers and the legacy BeforeToolHook share a single dispatch path.
func (a *Agent) execHookSubscriber() func(ToolCallEvent) *PermissionDecision {
	hookRegisteredMu.Lock()
	if !hookRegistered[a] {
		hookRegistered[a] = true
		EventBusInstance().On(func(e ToolCallEvent) *PermissionDecision {
			if e.Type == "tool_call" {
				allowed, reason := a.runExecBeforeHook(e.ToolCall)
				return &PermissionDecision{Allowed: allowed, Reason: reason}
			}
			a.runExecAfterHook(e.ToolCall, e.Output, e.Err)
			return nil
		})
	}
	hookRegisteredMu.Unlock()
	return PermissionHook
}

func (a *Agent) runBeforeToolHook(tc db.ToolCall) (bool, string) {
	a.execHookSubscriber()
	decision := EventBusInstance().Emit(ToolCallEvent{Type: "tool_call", ToolCall: tc})
	if decision != nil && !decision.Allowed {
		return false, decision.Reason
	}
	return true, ""
}

func (a *Agent) runExecBeforeHook(tc db.ToolCall) (bool, string) {
	if a.Config == nil || a.Config.BeforeToolHook == "" {
		return true, ""
	}

	cmd := exec.Command("bash", "-c", a.Config.BeforeToolHook)
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

	err := cmd.Run()
	if err != nil {
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
	a.execHookSubscriber()
	EventBusInstance().Emit(ToolCallEvent{Type: "tool_result", ToolCall: tc, Output: output, Err: toolErr})
	return a.runExecAfterHook(tc, output, toolErr)
}

func (a *Agent) runExecAfterHook(tc db.ToolCall, output string, toolErr error) (string, error) {
	if a.Config == nil || a.Config.AfterToolHook == "" {
		return output, toolErr
	}

	cmd := exec.Command("bash", "-c", a.Config.AfterToolHook)
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

	err := cmd.Run()
	if err != nil {
		reason := strings.TrimSpace(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return output, fmt.Errorf("after_tool_hook failed: %s", reason)
	}

	hookOutput := stdout.String()
	if hookOutput != "" {
		return hookOutput, nil
	}
	return output, toolErr
}
