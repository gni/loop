package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
)

// DebugLogger records complete execution traces, function calls, arguments,
// LLM payloads, and repetition statistics to a persistent file.
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

// NewDebugLogger enables sensitive execution tracing only for an explicit target.
// Set debug_log_file or LOOP_DEBUG_FILE to a path to opt in.
func NewDebugLogger(_ string, customPath string) *DebugLogger {
	target := tool.FirstNonEmpty(strings.TrimSpace(customPath), strings.TrimSpace(os.Getenv("LOOP_DEBUG_FILE")))
	if isDisabledDebugTarget(target) {
		return nil
	}
	if target == "" {
		return nil
	}

	if absTarget, err := filepath.Abs(target); err == nil {
		target = absTarget
	}

	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return nil
	}

	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug logging disabled: %v\n", err)
		return nil
	}

	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil
	}
	dl := &DebugLogger{
		filePath:       target,
		file:           f,
		fileReadCounts: make(map[string]int),
	}

	dl.writeRaw(fmt.Sprintf("\n%s\n[SESSION START: %s]\nDebug Log File: %s\n%s\n",
		strings.Repeat("=", 80),
		nowDebugTimestamp(),
		target,
		strings.Repeat("=", 80),
	))

	return dl
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

	sep := strings.Repeat("=", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] USER COMMAND (Session: %s)\nPrompt: %s\n%s\n",
		sep, nowDebugTimestamp(), sessionID, strings.TrimSpace(prompt), sep)

	dl.writeRaw(sb.String())
}

// LogLLMRequest records the payload and tool loadout sent to the LLM.
func (dl *DebugLogger) LogLLMRequest(sessionID string, iter int, model, endpoint string, messages []db.Message, tools []tool.Tool) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	sep := strings.Repeat("-", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] LLM REQUEST (Turn Iteration %d) (Session: %s)\nModel: %s | Endpoint: %s\nMessages Count: %d\n",
		sep, nowDebugTimestamp(), iter, sessionID, model, endpoint, len(messages))

	for i, m := range messages {
		contentPreview := strings.TrimSpace(m.Content)
		if len(contentPreview) > 300 {
			contentPreview = contentPreview[:300] + "... (truncated)"
		}
		fmt.Fprintf(&sb, "  [%d] Role: %-9s", i+1, m.Role)
		if m.ToolCallID != "" {
			fmt.Fprintf(&sb, " | ToolCallID: %s | Name: %s", m.ToolCallID, m.Name)
		}
		if len(m.ToolCalls) > 0 {
			tcNames := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				tcNames = append(tcNames, fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments))
			}
			fmt.Fprintf(&sb, " | Emitted ToolCalls: %s", strings.Join(tcNames, ", "))
		}
		fmt.Fprintf(&sb, "\n      Content: %q\n", contentPreview)
	}

	if len(tools) > 0 {
		toolNames := make([]string, 0, len(tools))
		for _, t := range tools {
			toolNames = append(toolNames, t.Function.Name)
		}
		fmt.Fprintf(&sb, "Tools Offered (%d): %s\n", len(tools), strings.Join(toolNames, ", "))
	}
	fmt.Fprintf(&sb, "%s\n", sep)

	dl.writeRaw(sb.String())
}

// LogLLMResponse records the LLM completion response, reasoning thoughts, and proposed tool calls.
func (dl *DebugLogger) LogLLMResponse(sessionID string, iter int, msg *db.Message, duration time.Duration) {
	if dl == nil || msg == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	sep := strings.Repeat("-", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] LLM RESPONSE (Turn Iteration %d) (Duration: %v) (Session: %s)\n",
		sep, nowDebugTimestamp(), iter, duration.Round(time.Millisecond), sessionID)

	if msg.ReasoningContent != "" {
		fmt.Fprintf(&sb, "Reasoning / Thoughts:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(msg.ReasoningContent), "\n", "\n  "))
	}

	if trimmed := strings.TrimSpace(msg.Content); trimmed != "" {
		fmt.Fprintf(&sb, "Assistant Content: %s\n", trimmed)
	}

	if len(msg.ToolCalls) > 0 {
		fmt.Fprintf(&sb, "Tool Calls Proposed (%d):\n", len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			formattedArgs := formatJSONOrRaw(tc.Function.Arguments)
			fmt.Fprintf(&sb, "  #%d [%s] Function: %s\n      Arguments: %s\n",
				i+1, tc.ID, tc.Function.Name, strings.ReplaceAll(formattedArgs, "\n", "\n      "))
		}
	} else {
		sb.WriteString("No Tool Calls Proposed (Final Turn Output)\n")
	}
	fmt.Fprintf(&sb, "%s\n", sep)

	dl.writeRaw(sb.String())
}

// LogToolExecution records a function execution with exact arguments, return result,
// duration, and detects repetitive file inspection patterns.
func (dl *DebugLogger) LogToolExecution(sessionID string, iter int, toolName string, arguments string, output string, err error, duration time.Duration) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	status := "SUCCESS"
	if err != nil {
		status = fmt.Sprintf("ERROR (%v)", err)
	}

	sep := strings.Repeat("-", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] TOOL EXECUTION: %s | Status: %s | Duration: %v\n",
		sep, nowDebugTimestamp(), toolName, status, duration.Round(time.Millisecond))

	formattedArgs := formatJSONOrRaw(arguments)
	fmt.Fprintf(&sb, "Arguments:\n  %s\n", strings.ReplaceAll(formattedArgs, "\n", "\n  "))

	if toolName == "read" {
		var readArgs struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(arguments), &readArgs)
		if targetFile := filetool.ExtractFilePath(arguments, readArgs.Path); targetFile != "" {
			dl.fileReadCounts[targetFile]++
			if count := dl.fileReadCounts[targetFile]; count > 1 {
				fmt.Fprintf(&sb, "⚠️  [RE-READ DETECTED #%d] Target file '%s' has been read %d times this turn! (offset=%d, limit=%d)\n",
					count, targetFile, count, readArgs.Offset, readArgs.Limit)
			}
		}
	}

	lines := strings.Split(output, "\n")
	fmt.Fprintf(&sb, "Output (%d bytes, %d lines):\n", len(output), len(lines))

	const maxOutputDebugLen = 4096
	if len(output) <= maxOutputDebugLen {
		fmt.Fprintf(&sb, "  %s\n", strings.ReplaceAll(strings.TrimRight(output, "\r\n"), "\n", "\n  "))
	} else {
		fmt.Fprintf(&sb, "  %s\n  ... [%d bytes omitted from debug display] ...\n  %s\n",
			strings.ReplaceAll(output[:2500], "\n", "\n  "),
			len(output)-3500,
			strings.ReplaceAll(output[len(output)-1000:], "\n", "\n  "),
		)
	}

	fmt.Fprintf(&sb, "%s\n", sep)

	dl.writeRaw(sb.String())
}

// LogRepetition records an alert when the loop breaker detects repetitive calls.
func (dl *DebugLogger) LogRepetition(sessionID string, toolName string, arguments string, count int, detail string) {
	if dl == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	sep := strings.Repeat("!", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] REPETITION WARNING | Function: %s | Repeated: %d times\nArguments: %s\n",
		sep, nowDebugTimestamp(), toolName, count, strings.TrimSpace(arguments))
	if detail != "" {
		fmt.Fprintf(&sb, "Detail: %s\n", detail)
	}
	fmt.Fprintf(&sb, "%s\n", sep)

	dl.writeRaw(sb.String())
}

// LogError records a persistence/config-save failure that the caller cannot surface to
// the user, so disk/memory divergence is at least observable in the debug log.
func (dl *DebugLogger) LogError(sessionID string, op string, err error) {
	if dl == nil || err == nil {
		return
	}
	dl.mu.Lock()
	defer dl.mu.Unlock()

	sep := strings.Repeat("~", 80)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s\n[%s] ERROR | Operation: %s | Session: %s\nError: %v\n%s\n",
		sep, nowDebugTimestamp(), op, sessionID, err, sep)

	dl.writeRaw(sb.String())
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

func formatJSONOrRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(empty)"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err == nil {
		return buf.String()
	}
	return raw
}
