package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"maquis/pkg/agent/tool"
	"maquis/pkg/db"
	"maquis/pkg/ui/style"
)

// LoadMemoryContext loads global (~/.maquis/MAQUIS.md) and project (MEMORY.md) memory context.
func (a *Agent) LoadMemoryContext() string {
	var sb strings.Builder

	// 1. Global Memory (~/.maquis/MAQUIS.md)
	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, ".maquis", "MAQUIS.md")
		if data, err := os.ReadFile(globalPath); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if len(trimmed) > 0 {
				sb.WriteString(fmt.Sprintf("\n\nUser Directives (%s):\nAdhere to these global preferences and personal mandates strictly:\n%s", globalPath, trimmed))
			}
		}
	}

	// 2. Project Memory (current workspace MEMORY.md)
	// We search in current directory or traverse up to git root or stop at workspace root
	wd, err := os.Getwd()
	if err == nil {
		dir := wd
		for {
			projectPath := filepath.Join(dir, "MEMORY.md")
			if data, err := os.ReadFile(projectPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\nProject Architecture & Learnings (%s):\nFollow these repository conventions and architectural decisions strictly:\n%s", projectPath, trimmed))
				}
				break
			}

			projectDotPath := filepath.Join(dir, ".maquis", "MEMORY.md")
			if data, err := os.ReadFile(projectDotPath); err == nil {
				trimmed := strings.TrimSpace(string(data))
				if len(trimmed) > 0 {
					sb.WriteString(fmt.Sprintf("\n\nProject Architecture & Learnings (%s):\nFollow these repository conventions and architectural decisions strictly:\n%s", projectDotPath, trimmed))
				}
				break
			}

			// Stop if we reach git root, workspace root, or root directory
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			if dir == a.WorkspaceRoot {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return sb.String()
}

// CalculateHistoryTokens calculates prompt and completion tokens for a conversation history.
// For each assistant turn, if PromptTokens > 0 it uses it; otherwise it estimates prompt tokens
// from all messages in the history preceding that turn. If CompletionTokens > 0 it uses it;
// otherwise it estimates completion tokens from Content, ReasoningContent, and ToolCalls.
func CalculateHistoryTokens(history []db.Message) (int, int) {
	var totalPrompt, totalCompletion int
	for i, m := range history {
		if m.Role != "assistant" {
			continue
		}
		hasPayload := m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0
		if !hasPayload && m.PromptTokens == 0 && m.CompletionTokens == 0 {
			continue
		}

		// Completion tokens
		if m.CompletionTokens > 0 {
			totalCompletion += m.CompletionTokens
		} else {
			chars := len(m.Content) + len(m.ReasoningContent)
			for _, tc := range m.ToolCalls {
				chars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
			comp := chars / 4
			if comp == 0 && chars > 0 {
				comp = 1
			}
			totalCompletion += comp
		}

		// Prompt tokens
		if m.PromptTokens > 0 {
			totalPrompt += m.PromptTokens
		} else {
			priorChars := 0
			for j := 0; j < i; j++ {
				prior := history[j]
				priorChars += len(prior.Content) + len(prior.ReasoningContent)
				for _, tc := range prior.ToolCalls {
					priorChars += len(tc.Function.Name) + len(tc.Function.Arguments)
				}
			}
			p := priorChars / 4
			if p == 0 && priorChars > 0 {
				p = 1
			}
			totalPrompt += p
		}
	}
	return totalPrompt, totalCompletion
}

// GetGlobalTokens returns the prompt and completion tokens from the latest assistant message.
func (a *Agent) GetGlobalTokens(messages []db.Message, allowedTools []string) (int, int) {
	prompt, completion, _ := a.GetGlobalTokenUsage(messages, allowedTools)
	return prompt, completion
}

// GetGlobalTokenUsage extracts measured token counts returned by the OpenAI API from message history.
// If the latest assistant turn only reported completion tokens or if no assistant turn exists yet,
// it computes an estimate based on prompt characters and active tools.
func (a *Agent) GetGlobalTokenUsage(messages []db.Message, _ []string) (int, int, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
		if message.Role == "assistant" && hasPayload && (message.PromptTokens > 0 || message.CompletionTokens > 0) {
			if message.PromptTokens > 0 {
				return message.PromptTokens, message.CompletionTokens, false
			}
			priorChars := 0
			for j := 0; j < i; j++ {
				prior := messages[j]
				priorChars += len(prior.Content) + len(prior.ReasoningContent)
				for _, tc := range prior.ToolCalls {
					priorChars += len(tc.Function.Name) + len(tc.Function.Arguments)
				}
			}
			estPrompt := priorChars / 4
			if estPrompt == 0 && priorChars > 0 {
				estPrompt = 1
			}
			return estPrompt, message.CompletionTokens, true
		}
	}

	if len(messages) > 0 {
		totalChars := 0
		for _, m := range messages {
			totalChars += len(m.Content) + len(m.ReasoningContent)
			for _, tc := range m.ToolCalls {
				totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		estPrompt := totalChars / 4
		if estPrompt == 0 && totalChars > 0 {
			estPrompt = 1
		}
		if estPrompt > 0 {
			return estPrompt, 0, true
		}
	}

	return 0, 0, false
}

// GetSessionTotalCompletionTokens calculates the global sum of completion tokens generated across all assistant turns in the session.
func (a *Agent) GetSessionTotalCompletionTokens(messages []db.Message) int {
	total := 0
	for _, m := range messages {
		if m.Role == "assistant" {
			hasPayload := m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0
			if hasPayload {
				if m.CompletionTokens > 0 {
					total += m.CompletionTokens
				} else {
					chars := len(m.Content) + len(m.ReasoningContent)
					for _, tc := range m.ToolCalls {
						chars += len(tc.Function.Name) + len(tc.Function.Arguments)
					}
					comp := chars / 4
					if comp == 0 && chars > 0 {
						comp = 1
					}
					total += comp
				}
			}
		}
	}
	return total
}

// GetLatestAssistantCompletionTokens returns the completion tokens of the latest assistant message.
func (a *Agent) GetLatestAssistantCompletionTokens(messages []db.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == "assistant" {
			hasPayload := message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0
			if hasPayload && message.CompletionTokens > 0 {
				return message.CompletionTokens
			}
		}
	}
	return 0
}

// FormatDefensiveError turns syntax errors into descriptive, action-oriented correction prompts
func FormatDefensiveError(toolName string, err error) string {
	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)

	if strings.Contains(errStr, "Recommendation:") {
		return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s", toolName, errStr)
	}
	if strings.Contains(lowerErr, "loop detected") || strings.Contains(lowerErr, "agent halted") {
		return fmt.Sprintf("System Alert: Your execution of '%s' was blocked: %s", toolName, errStr)
	}

	var suggestion string
	if (strings.Contains(lowerErr, "oldtext block") && strings.Contains(lowerErr, "not found")) ||
		strings.Contains(lowerErr, "targetcontent not found") {
		suggestion = "The file exists, but oldText does not match its current contents. Read the file again, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write."
	} else if (strings.Contains(lowerErr, "task") && (strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no task"))) ||
		toolName == "task_status" || toolName == "task_kill" {
		suggestion = "The background task ID was not found. Check background tasks list or events to inspect valid task IDs. Do not search the filesystem for task IDs."
	} else if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "no such file") {
		suggestion = "Inspect your immediate working directory structure using 'find' or 'list' or check the file path. Ensure the file actually exists before calling this tool."
	} else if strings.Contains(lowerErr, "escapes workspace") || strings.Contains(lowerErr, "security violation") {
		suggestion = "Verify that the path is relative or inside the current workspace. Escaping the workspace is blocked."
	} else if strings.Contains(lowerErr, "command failed") || strings.Contains(lowerErr, "exit status") {
		suggestion = "Review the command syntax and arguments. If the command depends on specific environment setups or files, verify they are present."
	} else if strings.HasPrefix(lowerErr, "unknown tool:") {
		suggestion = "Inspect <tools> in system instructions for valid tool names. For example, use 'write' instead of 'write_path' or 'write_file', and 'edit' instead of 'edit_file'."
	} else {
		suggestion = "Ensure arguments match the schema parameters exactly, and that any files/folders referred to exist and are spelled correctly."
	}

	return fmt.Sprintf("System Alert: Your execution of '%s' failed due to: %s\nRecommendation: %s", toolName, errStr, suggestion)
}

// FormatToolExecutionFailure preserves useful tool diagnostics while adding the
// action-oriented failure context expected by the agent and terminal UI.
func FormatToolExecutionFailure(toolName, output string, err error) string {
	if err == nil {
		return strings.TrimRight(output, "\r\n")
	}
	diagnostic := strings.Trim(output, "\r\n")
	if toolName == "bash" {
		if diagnostic != "" {
			if strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
				return diagnostic
			}
			if !strings.HasPrefix(diagnostic, "[Command Failed") && !strings.HasPrefix(diagnostic, "Command failed") && !strings.HasPrefix(diagnostic, "Error:") {
				errMsg := err.Error()
				if strings.HasPrefix(errMsg, "command failed: ") {
					errMsg = strings.TrimPrefix(errMsg, "command failed: ")
				}
				return fmt.Sprintf("[Command Failed: %s]\n%s", errMsg, diagnostic)
			}
			return diagnostic
		}
		return fmt.Sprintf("[Command Failed: %s]", err.Error())
	}
	alert := FormatDefensiveError(toolName, err)
	if strings.TrimSpace(diagnostic) == "" || strings.TrimSpace(diagnostic) == strings.TrimSpace(err.Error()) {
		return alert
	}
	return diagnostic + "\n\n" + alert
}

// ParseFallbackToolCalls extracts XML-style fallback tool calls from plain assistant message content
func ParseFallbackToolCalls(content string) []db.ToolCall {
	var toolCalls []db.ToolCall

	// 1. Match <tool_call name="name">args</tool_call>
	re1 := regexp.MustCompile(`<tool_call\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/tool_call>`)
	matches1 := re1.FindAllStringSubmatch(content, -1)
	for _, match := range matches1 {
		if len(match) >= 3 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			toolCalls = append(toolCalls, buildToolCall(name, args))
		}
	}

	// 2. Match <tool:name>args</tool:name>
	re2 := regexp.MustCompile(`<tool:([a-zA-Z0-9_\-]+)>(?s:(.*?))<\/tool:([a-zA-Z0-9_\-]+)>`)
	matches2 := re2.FindAllStringSubmatch(content, -1)
	for _, match := range matches2 {
		if len(match) >= 4 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			closeName := match[3]
			if name == closeName {
				toolCalls = append(toolCalls, buildToolCall(name, args))
			}
		}
	}

	// 3. Match <execute name="name">args</execute>
	re3 := regexp.MustCompile(`<execute\s+name=["']([a-zA-Z0-9_\-]+)["']>(?s:(.*?))<\/execute>`)
	matches3 := re3.FindAllStringSubmatch(content, -1)
	for _, match := range matches3 {
		if len(match) >= 3 {
			name := match[1]
			args := strings.TrimSpace(match[2])
			toolCalls = append(toolCalls, buildToolCall(name, args))
		}
	}

	// 4. Match the Hermes/Qwen chat-template dialect:
	//   <tool_call>
	//    <function=bash>
	//    <parameter=command>value</parameter>
	//    </function>
	//   </tool_call>
	// Some models emit a degraded form of this dialect where tag-closing '>'
	// is replaced by a newline and the parameter '=' is dropped entirely
	// (e.g. "<function=bash\n<parametercommand\ncmd\n</parameter\n</function\n").
	// Both the canonical and degraded forms must parse, otherwise the call is
	// silently dropped and the agent never executes it.
	reBlock := regexp.MustCompile(`(?s)<tool_call\s*>?(.*?)(?:<\/tool_call\s*>|$)`)
	reFunc := regexp.MustCompile(`(?s)<function=([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/function(?:\s*>|\s*\n|$)|$)`)
	// Optional separator: the degraded form drops '=' entirely ("<parametercommand").
	reParam := regexp.MustCompile(`(?s)<parameter[=\s]*([a-zA-Z0-9_\-]+)(?:\s*>|\s*\n|$)(.*?)(?:<\/parameter(?:\s*>|\s*\n|$)|$)`)

	parseHermesFunction := func(name, body string) db.ToolCall {
		params := make(map[string]string)
		for _, p := range reParam.FindAllStringSubmatch(body, -1) {
			params[p[1]] = strings.TrimSpace(p[2])
		}
		if len(params) == 0 {
			return buildToolCall(name, strings.TrimSpace(body))
		}
		args, err := json.Marshal(params)
		if err != nil {
			return buildToolCall(name, strings.TrimSpace(body))
		}
		return buildToolCall(name, string(args))
	}

	blocks := reBlock.FindAllStringSubmatch(content, -1)
	if len(blocks) > 0 {
		for _, match := range blocks {
			if len(match) < 2 {
				continue
			}
			body := match[1]
			for _, funcMatch := range reFunc.FindAllStringSubmatch(body, -1) {
				if len(funcMatch) >= 3 {
					toolCalls = append(toolCalls, parseHermesFunction(funcMatch[1], funcMatch[2]))
				}
			}
		}
	}

	if len(toolCalls) == 0 {
		// Degenerate output without the tool_call wrapper
		for _, match := range reFunc.FindAllStringSubmatch(content, -1) {
			if len(match) >= 3 {
				toolCalls = append(toolCalls, parseHermesFunction(match[1], match[2]))
			}
		}
	}

	return toolCalls
}

func buildToolCall(name string, args string) db.ToolCall {
	name = tool.NormalizeName(name)
	// Try parsing as JSON first
	var temp map[string]interface{}
	isJSON := json.Unmarshal([]byte(args), &temp) == nil

	finalArgs := args
	if !isJSON {
		// Escape the args string for JSON
		escapedArgs, _ := json.Marshal(args)
		escapedArgsStr := string(escapedArgs) // this is a JSON quoted string like "my command"

		// Wrap according to tool name
		switch {
		case name == "bash" || name == "ls":
			finalArgs = fmt.Sprintf(`{"command": %s}`, escapedArgsStr)
		case name == "read":
			finalArgs = fmt.Sprintf(`{"path": %s}`, escapedArgsStr)
		case name == "grep":
			finalArgs = fmt.Sprintf(`{"pattern": %s}`, escapedArgsStr)
		case name == "load_skill":
			finalArgs = fmt.Sprintf(`{"name": %s}`, escapedArgsStr)
		case name == "task_status" || name == "task_kill":
			finalArgs = fmt.Sprintf(`{"task_id": %s}`, escapedArgsStr)
		case strings.HasPrefix(name, "subagent__"):
			finalArgs = fmt.Sprintf(`{"prompt": %s}`, escapedArgsStr)
		default:
			// Fallback: wrap as a generic string argument
			finalArgs = fmt.Sprintf(`{"arguments": %s}`, escapedArgsStr)
		}
	}

	tc := db.ToolCall{
		ID:   fmt.Sprintf("call_fallback_%s", db.NewUUID()[:8]),
		Type: "function",
	}
	tc.Function.Name = name
	tc.Function.Arguments = finalArgs
	return tc
}

// TruncateRunes safely truncates a string to maxRunes without slicing multi-byte UTF-8 runes.
func TruncateRunes(s string, maxRunes int) string {
	return style.TruncateRunes(s, maxRunes)
}

// echoWrappers are the ways a model wraps an echoed prompt before thinking.
var echoWrappers = []string{`"`, "'", "`", "> "}

// stripPromptEcho removes a leading echoed prompt (optionally wrapped) plus the
// closing wrapper and any separator punctuation that follows it. The closing quote
// and separator are part of the echo, not the thought, which is why a stream looked
// like it "started with a quote/dot".
func stripPromptEcho(reasoning, normPrompt string) string {
	if normPrompt == "" || reasoning == "" {
		return reasoning
	}
	clean := strings.TrimLeft(reasoning, "\r\n\t ")
	lowerPrompt := strings.ToLower(normPrompt)

	for _, wrapper := range echoWrappers {
		if !strings.HasPrefix(clean, wrapper) {
			continue
		}
		body := strings.TrimPrefix(clean, wrapper)
		if strings.HasPrefix(strings.ToLower(body), lowerPrompt) {
			return trimEchoResidue(body[len(normPrompt):], wrapper)
		}
	}
	if strings.HasPrefix(strings.ToLower(clean), lowerPrompt) {
		return trimEchoResidue(clean[len(normPrompt):], "")
	}
	return reasoning
}

// trimEchoResidue drops the closing wrapper and the separator punctuation the model
// uses to transition from the echo into its actual thought.
func trimEchoResidue(rest, wrapper string) string {
	rest = strings.TrimLeft(rest, "\r\n\t ")
	if wrapper != "" && strings.HasPrefix(rest, wrapper) {
		rest = strings.TrimPrefix(rest, wrapper)
	}
	rest = strings.TrimLeft(rest, "\r\n\t ")
	for len(rest) > 0 && strings.ContainsRune(".:-,", rune(rest[0])) {
		rest = rest[1:]
		rest = strings.TrimLeft(rest, "\r\n\t ")
	}
	return rest
}

// StripEchoedPrompt strips leading echoed prompt text and trailing newlines/whitespace
// from model reasoning content if the model begins thinking by repeating the user's prompt.
func StripEchoedPrompt(reasoning, prompt string) string {
	return stripPromptEcho(reasoning, strings.TrimSpace(prompt))
}

// PromptEchoFilter suppresses an echoed prompt as it streams. Post-hoc stripping was
// not enough: chunks are printed the moment they arrive, so a model that opens its
// reasoning by repeating the prompt printed the echo (and the trailing quote and
// separator punctuation) before anything could be removed. The filter holds back only
// the runes needed to decide, then passes everything through.
type PromptEchoFilter struct {
	normPrompt  string
	lowerPrompt string
	held        strings.Builder
	phase       int // 0 deciding, 1 skipping echo residue, 2 passing through
}

func NewPromptEchoFilter(prompt string) *PromptEchoFilter {
	norm := strings.TrimSpace(prompt)
	return &PromptEchoFilter{normPrompt: norm, lowerPrompt: strings.ToLower(norm)}
}

// Write returns the portion of chunk that is safe to print.
func (f *PromptEchoFilter) Write(chunk string) string {
	if f.phase == 2 || f.normPrompt == "" {
		return chunk
	}
	f.held.WriteString(chunk)

	if f.phase == 0 {
		clean := strings.TrimLeft(f.held.String(), "\r\n\t ")

		// Full echo confirmed (with or without a wrapper): consume it, then skip residue.
		// A bare wrapper cannot be judged yet, so it stays held until the echoed prompt
		// either completes or is ruled out.
		for _, wrapper := range echoWrappers {
			if strings.HasPrefix(clean, wrapper) {
				body := clean[len(wrapper):]
				if strings.HasPrefix(strings.ToLower(body), f.lowerPrompt) {
					f.held.Reset()
					f.phase = 1
					return f.skipResidue(body[len(f.normPrompt):])
				}
				if len(body) < len(f.lowerPrompt) && strings.HasPrefix(f.lowerPrompt, strings.ToLower(body)) {
					return ""
				}
			}
		}
		if strings.HasPrefix(strings.ToLower(clean), f.lowerPrompt) {
			f.held.Reset()
			f.phase = 1
			return f.skipResidue(clean[len(f.normPrompt):])
		}

		// Ambiguous: the echo could still complete in a later chunk, so hold.
		if len(clean) < len(f.lowerPrompt) && strings.HasPrefix(f.lowerPrompt, strings.ToLower(clean)) {
			return ""
		}

		// Confirmed not an echo: emit what was held.
		f.phase = 2
		f.held.Reset()
		return clean
	}

	return f.skipResidue(f.held.String())
}

// skipResidue consumes the closing wrapper and separator punctuation that separates the
// echo from the real thought, and emits from the first character of that thought.
func (f *PromptEchoFilter) skipResidue(s string) string {
	for _, wrapper := range echoWrappers {
		s = strings.TrimLeft(s, "\r\n\t ")
		if s == "" {
			f.held.Reset()
			return ""
		}
		if strings.HasPrefix(s, wrapper) {
			s = s[len(wrapper):]
			break
		}
	}
	s = strings.TrimLeft(s, "\r\n\t ")
	for len(s) > 0 && strings.ContainsRune(".:-,", rune(s[0])) {
		s = s[1:]
		s = strings.TrimLeft(s, "\r\n\t ")
	}
	if s == "" {
		f.held.Reset()
		return ""
	}
	f.phase = 2
	f.held.Reset()
	return s
}

// Flush returns anything still held when the stream ends before the decision resolved.
func (f *PromptEchoFilter) Flush() string {
	if f.phase == 1 {
		f.held.Reset()
		return ""
	}
	out := f.held.String()
	f.held.Reset()
	return out
}

// Tool classification lives in intent.go (single source of truth).

var llmControlTokenRegexes = []*regexp.Regexp{
	regexp.MustCompile(`</?atem:[^>\n<]*>?`),
	regexp.MustCompile(`</?atem:[^<\n]*`),
	regexp.MustCompile(`<\|(?:eom|im_start|im_end|start|message|endoftext|assistant|user|system)[^>|]*\|?>`),
	regexp.MustCompile(`<\|[^>\n|]+\|>`),
}

// SanitizeLLMControlTokens removes leaked provider control tokens and ChatML tags from text.
func SanitizeLLMControlTokens(s string) string {
	for _, re := range llmControlTokenRegexes {
		s = re.ReplaceAllString(s, "")
	}
	return s
}
