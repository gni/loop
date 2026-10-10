package agent

import (
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

// DebugLogError records a persistence/config error that would otherwise be discarded.
func (a *Agent) DebugLogError(sessionID, op string, err error) {
	if a == nil || a.DebugLogger == nil || err == nil {
		return
	}
	a.DebugLogger.LogError(sessionID, op, err)
}

// GetDebugLogPath returns the file path of the debug log if enabled.
func (a *Agent) GetDebugLogPath() string {
	if a == nil || a.DebugLogger == nil {
		return ""
	}
	return a.DebugLogger.FilePath()
}

// DebugLogUserCommand records a submitted prompt to the debug log.
func (a *Agent) DebugLogUserCommand(sessionID, prompt string) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogUserCommand(sessionID, prompt)
}

// DebugLogLLMRequest records outgoing messages and tools payload to the debug log.
func (a *Agent) DebugLogLLMRequest(sessionID string, iter int, model, endpoint string, messages []db.Message, tools []tool.Tool) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogLLMRequest(sessionID, iter, model, endpoint, messages, tools)
}

// DebugLogLLMResponse records the LLM completion and proposed tool calls.
func (a *Agent) DebugLogLLMResponse(sessionID string, iter int, msg *db.Message, duration time.Duration) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogLLMResponse(sessionID, iter, msg, duration)
}

// DebugLogToolExecution records tool execution input, output, errors, and re-read detection.
func (a *Agent) DebugLogToolExecution(sessionID string, iter int, toolName string, arguments string, output string, err error, duration time.Duration) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogToolExecution(sessionID, iter, toolName, arguments, output, err, duration)
}

// DebugLogRepetition records repetition circuit breaker warnings to the debug log.
func (a *Agent) DebugLogRepetition(sessionID string, toolName string, arguments string, count int, detail string) {
	if a == nil || a.DebugLogger == nil {
		return
	}
	a.DebugLogger.LogRepetition(sessionID, toolName, arguments, count, detail)
}
