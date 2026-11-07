package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

// DebugLogger records metadata only; prompt, reasoning, arguments, outputs,
// paths, and error messages must never be persisted in debug traces.
type DebugLogger struct {
	mu             sync.Mutex
	filePath       string
	file           *os.File
	fileReadCounts map[string]int
}

func isDisabledDebugTarget(target string) bool {
	switch strings.ToLower(target) {
	case "off", "none", "false", "disabled", "/dev/null", "null":
		return true
	default:
		return false
	}
}

// ResolveDebugTarget resolves the canonical debug log file path, or "" if disabled.
func ResolveDebugTarget(customPath string) string {
	target := tool.FirstNonEmpty(strings.TrimSpace(customPath), strings.TrimSpace(os.Getenv("LOOP_DEBUG_FILE")))
	if isDisabledDebugTarget(target) || target == "" {
		return ""
	}
	if absTarget, err := filepath.Abs(target); err == nil {
		return absTarget
	}
	return target
}

// NewDebugLogger enables metadata-only execution tracing for an explicit target.
// Set debug_log_file or LOOP_DEBUG_FILE to a path to opt in.
func NewDebugLogger(_ string, customPath string) *DebugLogger {
	logger, err := openDebugLogger(customPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug logging disabled: %v\n", err)
	}
	return logger
}

func openDebugLogger(customPath string) (*DebugLogger, error) {
	target := ResolveDebugTarget(customPath)
	if target == "" {
		return nil, nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}

	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	dl := &DebugLogger{
		filePath:       target,
		file:           f,
		fileReadCounts: make(map[string]int),
	}

	if _, err := f.WriteString(fmt.Sprintf("\n%s\n[SESSION START: %s]\nMetadata logging only\n%s\n", strings.Repeat("=", 80), nowDebugTimestamp(), strings.Repeat("=", 80))); err != nil {
		_ = f.Close()
		return nil, err
	}

	return dl, nil
}

func nowDebugTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

// FilePath returns the absolute path to the debug log file.
func (dl *DebugLogger) FilePath() string {
	if dl == nil {
		return ""
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.filePath
}

func (dl *DebugLogger) writeRaw(content string) {
	if dl == nil || dl.file == nil {
		return
	}
	_, _ = dl.file.WriteString(content)
	_ = dl.file.Sync()
}

// ResetTurnCounts resets per-turn counters (such as file reread trackers).
func (dl *DebugLogger) ResetTurnCounts() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.fileReadCounts = make(map[string]int)
}

// LogUserCommand records a newly submitted prompt or command.
func (dl *DebugLogger) LogUserCommand(sessionID string, prompt string) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.fileReadCounts = make(map[string]int)
	dl.writeRaw(fmt.Sprintf("[%s] USER COMMAND session=%s prompt_bytes=%d [content omitted]\n", nowDebugTimestamp(), sessionID, len(prompt)))
}

// LogLLMRequest records the payload and tool loadout sent to the LLM.
func (dl *DebugLogger) LogLLMRequest(sessionID string, iter int, model, endpoint string, messages []db.Message, tools []tool.Tool) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	var contentBytes, toolCalls int
	for _, m := range messages {
		contentBytes += len(m.Content) + len(m.ReasoningContent)
		toolCalls += len(m.ToolCalls)
	}
	dl.writeRaw(fmt.Sprintf("[%s] LLM REQUEST session=%s iteration=%d messages=%d content_bytes=%d tool_calls=%d offered_tools=%d [payload omitted]\n", nowDebugTimestamp(), sessionID, iter, len(messages), contentBytes, toolCalls, len(tools)))
}

// LogLLMResponse records the LLM completion response, reasoning thoughts, and proposed tool calls.
func (dl *DebugLogger) LogLLMResponse(sessionID string, iter int, msg *db.Message, duration time.Duration) {
	if dl == nil || msg == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.writeRaw(fmt.Sprintf("[%s] LLM RESPONSE session=%s iteration=%d duration=%v content_bytes=%d reasoning_bytes=%d tool_calls=%d [content omitted]\n", nowDebugTimestamp(), sessionID, iter, duration.Round(time.Millisecond), len(msg.Content), len(msg.ReasoningContent), len(msg.ToolCalls)))
}

// LogToolExecution records a function execution with exact arguments, return result,
// duration, and detects repetitive file inspection patterns.
func (dl *DebugLogger) LogToolExecution(sessionID string, iter int, toolName string, arguments string, output string, err error, duration time.Duration) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	status := "success"
	if err != nil {
		status = "error"
	}
	if toolName == "read" {
		// Count reads without persisting paths or other sensitive arguments.
		dl.fileReadCounts["read"]++
	}
	dl.writeRaw(fmt.Sprintf("[%s] TOOL session=%s iteration=%d tool=%s status=%s duration=%v argument_bytes=%d output_bytes=%d [payload omitted]\n", nowDebugTimestamp(), sessionID, iter, toolName, status, duration.Round(time.Millisecond), len(arguments), len(output)))
}

// LogRepetition records an alert when the loop breaker detects repetitive calls.
func (dl *DebugLogger) LogRepetition(sessionID string, toolName string, arguments string, count int, detail string) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.writeRaw(fmt.Sprintf("[%s] REPETITION session=%s tool=%s count=%d [arguments and detail omitted]\n", nowDebugTimestamp(), sessionID, toolName, count))
}

// LogError records a persistence/config-save failure that the caller cannot surface to
// the user, so disk/memory divergence is at least observable in the debug log.
func (dl *DebugLogger) LogError(sessionID string, op string, err error) {
	if dl == nil || err == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	dl.writeRaw(fmt.Sprintf("[%s] ERROR session=%s operation=%s error_type=%T [message omitted]\n", nowDebugTimestamp(), sessionID, op, err))
}

// Close flushes and closes the underlying debug log file.
func (dl *DebugLogger) Close() {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()
	if dl.file != nil {
		_ = dl.file.Sync()
		_ = dl.file.Close()
		dl.file = nil
	}
}
