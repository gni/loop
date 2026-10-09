package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// LoadMemoryContext loads global (~/.loop/LOOP.md) and project (MEMORY.md) memory context.
func (a *Agent) LoadMemoryContext() string {
	var sb strings.Builder

	// 1. Global Memory (~/.loop/LOOP.md)
	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, ".loop", "LOOP.md")
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

			projectDotPath := filepath.Join(dir, ".loop", "MEMORY.md")
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

func messageChars(m db.Message) int {
	chars := len(m.Content) + len(m.ReasoningContent)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return chars
}

// EstimateMessageTokens provides a calibrated token count estimation for messages.
// It uses an average of 3.2 characters per token to account for code, JSON, and whitespace.
func EstimateMessageTokens(m db.Message) int {
	chars := messageChars(m)
	if chars == 0 {
		return 0
	}
	tokens := int(float64(chars) / 3.2)
	if tokens == 0 && chars > 0 {
		return 1
	}
	return tokens
}

// EstimateMessagesTokens computes the total estimated tokens across a list of messages.
func EstimateMessagesTokens(messages []db.Message) int {
	total := 0
	for _, m := range messages {
		total += EstimateMessageTokens(m)
	}
	return total
}

// DefaultContextTiers defines progressive context tiers standard in local LLM runtimes (32K -> 64K -> 128K -> 262K).
var DefaultContextTiers = []int{32768, 65536, 131072, 262144}

// GetAdaptiveContextLimit dynamically selects the optimal context tier for the current workload,
// bounded by the server's detected limits and user configuration.
func GetAdaptiveContextLimit(currentTokens int, serverLimit int, configuredLimit int, minWindow int, autoAdapt bool) int {
	maxCeiling := configuredLimit
	if maxCeiling <= 0 {
		maxCeiling = 128000
	}
	if serverLimit > 0 {
		if serverLimit < maxCeiling || configuredLimit == 128000 {
			maxCeiling = serverLimit
		}
	}

	if !autoAdapt {
		return maxCeiling
	}

	if minWindow <= 0 {
		minWindow = 32768
	}
	if minWindow > maxCeiling {
		minWindow = maxCeiling
	}

	reserveTokens := 4096
	neededTokens := currentTokens + reserveTokens

	var tiers []int
	tiers = append(tiers, minWindow)
	for _, t := range DefaultContextTiers {
		if t > minWindow && t <= maxCeiling {
			tiers = append(tiers, t)
		}
	}
	if len(tiers) == 0 || tiers[len(tiers)-1] < maxCeiling {
		tiers = append(tiers, maxCeiling)
	}

	for _, tier := range tiers {
		if tier >= neededTokens {
			if tier > maxCeiling {
				return maxCeiling
			}
			return tier
		}
	}

	return maxCeiling
}

// CompactHistoricalToolOutputs condenses verbose tool outputs from completed earlier turns,
// preserving the latest turn's tool outputs in full to protect prefix cache and prevent token explosion.
func CompactHistoricalToolOutputs(messages []db.Message) []db.Message {
	if len(messages) == 0 {
		return messages
	}

	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	out := make([]db.Message, len(messages))
	for i, m := range messages {
		// Only compact historical tool outputs that occur before the latest assistant turn
		if m.Role == "tool" && lastAssistantIdx != -1 && i < lastAssistantIdx && len(m.Content) > 1000 {
			msgCopy := m
			lines := strings.Split(m.Content, "\n")
			if len(lines) > 20 {
				head := strings.Join(lines[:10], "\n")
				tail := strings.Join(lines[len(lines)-5:], "\n")
				msgCopy.Content = fmt.Sprintf("%s\n\n[... %d lines omitted from historical '%s' output to preserve context cache; call '%s' again if needed ...]\n\n%s", head, len(lines)-15, m.Name, m.Name, tail)
			} else {
				msgCopy.Content = m.Content[:750] + fmt.Sprintf("\n\n[... output truncated from historical '%s' to preserve context cache ...]\n\n", m.Name) + m.Content[len(m.Content)-250:]
			}
			out[i] = msgCopy
		} else {
			out[i] = m
		}
	}

	return out
}

func estimateCompletionTokens(m db.Message) int {
	if m.CompletionTokens > 0 {
		return m.CompletionTokens
	}
	chars := messageChars(m)
	comp := chars / 4
	if comp == 0 && chars > 0 {
		comp = 1
	}
	return comp
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
		totalCompletion += estimateCompletionTokens(m)

		// Prompt tokens
		if m.PromptTokens > 0 {
			totalPrompt += m.PromptTokens
		} else {
			priorChars := 0
			for j := 0; j < i; j++ {
				priorChars += messageChars(history[j])
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
				total += estimateCompletionTokens(m)
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
	if strings.Contains(lowerErr, "not unique") {
		suggestion = "The oldText block matches multiple locations in the file. Include more surrounding lines in oldText to make it unique, or read the file again to copy the exact unique block."
	} else if (strings.Contains(lowerErr, "oldtext block") && strings.Contains(lowerErr, "not found")) ||
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

// echoWrappers are the ways a model wraps an echoed prompt before thinking or writing.
var echoWrappers = []string{`"`, "'", "`", "> ", "# ", "## ", "### ", "#### ", "##### ", "###### ", "**"}

func normalizeDashVariants(s string) string {
	s = strings.ReplaceAll(s, `\u2014`, "-")
	s = strings.ReplaceAll(s, "\u2014", "-") // em-dash —
	s = strings.ReplaceAll(s, "\u2013", "-") // en-dash –
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func matchNormalizedPrefix(text, target string) int {
	normTarget := normalizeDashVariants(strings.ToLower(strings.TrimSpace(target)))
	if normTarget == "" {
		return -1
	}

	var normText strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], `\u2014`) {
			normText.WriteString("-")
			i += 6
		} else {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == '\u2014' || r == '\u2013' {
				normText.WriteString("-")
			} else {
				normText.WriteRune(unicode.ToLower(r))
			}
			i += size
		}
		currentNorm := normText.String()
		for strings.Contains(currentNorm, "--") {
			currentNorm = strings.ReplaceAll(currentNorm, "--", "-")
		}
		if currentNorm == normTarget {
			return i
		}
	}
	return -1
}

func extractPromptTargets(prompt string) []string {
	norm := strings.TrimSpace(prompt)
	if norm == "" {
		return nil
	}
	seen := make(map[string]bool)
	var targets []string
	addTarget := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}

	addTarget(norm)

	// If prompt has multiple lines, also match the title/first line
	lines := strings.Split(norm, "\n")
	if len(lines) > 1 {
		firstLine := strings.TrimSpace(lines[0])
		if len(firstLine) >= 3 {
			addTarget(firstLine)
			cleanFirst := strings.TrimLeft(firstLine, "#*_ \t\"'`")
			if len(cleanFirst) >= 3 {
				addTarget(cleanFirst)
			}
		}
	}

	// Also add title with leading markdown heading stripped if present
	cleanNorm := strings.TrimLeft(norm, "#*_ \t\"'`")
	if len(cleanNorm) >= 3 {
		addTarget(cleanNorm)
	}

	return targets
}

func matchPromptInText(clean string, targets []string) (matchedTarget string, remainder string, wrapper string) {
	for _, target := range targets {
		for _, w := range echoWrappers {
			if strings.HasPrefix(strings.ToLower(clean), strings.ToLower(w)) {
				body := clean[len(w):]
				if cut := matchNormalizedPrefix(body, target); cut != -1 {
					return target, body[cut:], w
				}
			}
		}

		if cut := matchNormalizedPrefix(clean, target); cut != -1 {
			return target, clean[cut:], ""
		}
	}
	return "", "", ""
}

// stripPromptEcho removes a leading echoed prompt (optionally wrapped) plus the
// closing wrapper and any separator punctuation that follows it.
func stripPromptEcho(reasoning, normPrompt string) string {
	if normPrompt == "" || reasoning == "" {
		return reasoning
	}
	clean := strings.TrimLeft(reasoning, "\r\n\t ")
	targets := extractPromptTargets(normPrompt)

	_, rem, wrapper := matchPromptInText(clean, targets)
	if rem != "" || wrapper != "" {
		return trimEchoResidue(rem, wrapper)
	}
	return reasoning
}

// trimEchoResidue drops the closing wrapper and the separator punctuation the model
// uses to transition from the echo into its actual thought or text.
func trimEchoResidue(rest, wrapper string) string {
	rest = strings.TrimLeft(rest, "\r\n\t ")
	if wrapper != "" && strings.HasPrefix(rest, wrapper) {
		rest = strings.TrimPrefix(rest, wrapper)
	}
	rest = strings.TrimLeft(rest, "\r\n\t ")
	for len(rest) > 0 && strings.ContainsRune(".:-,#", rune(rest[0])) {
		rest = rest[1:]
		rest = strings.TrimLeft(rest, "\r\n\t ")
	}
	return rest
}

// StripEchoedPrompt strips leading echoed prompt text and trailing newlines/whitespace
// from model content if the model begins by repeating the user's prompt or title.
func StripEchoedPrompt(content, prompt string) string {
	return stripPromptEcho(content, strings.TrimSpace(prompt))
}

// PromptEchoFilter suppresses an echoed prompt as it streams. Post-hoc stripping was
// not enough: chunks are printed the moment they arrive, so a model that opens its
// response by repeating the prompt printed the echo (and the trailing quote and
// separator punctuation) before anything could be removed. The filter holds back only
// the runes needed to decide, then passes everything through.
type PromptEchoFilter struct {
	normPrompt  string
	targets     []string
	held        strings.Builder
	phase       int // 0 deciding, 1 skipping echo residue, 2 passing through
}

func NewPromptEchoFilter(prompt string) *PromptEchoFilter {
	norm := strings.TrimSpace(prompt)
	targets := extractPromptTargets(norm)
	return &PromptEchoFilter{normPrompt: norm, targets: targets}
}

// Write returns the portion of chunk that is safe to print.
func (f *PromptEchoFilter) Write(chunk string) string {
	if f.phase == 2 || f.normPrompt == "" || len(f.targets) == 0 {
		return chunk
	}
	f.held.WriteString(chunk)

	if f.phase == 0 {
		clean := strings.TrimLeft(f.held.String(), "\r\n\t ")

		matchedTarget, rem, _ := matchPromptInText(clean, f.targets)
		if matchedTarget != "" {
			f.held.Reset()
			f.phase = 1
			return f.skipResidue(rem)
		}

		cleanDash := normalizeDashVariants(clean)
		lowerCleanDash := strings.ToLower(cleanDash)

		// Ambiguous: the echo could still complete in a later chunk, so hold.
		for _, target := range f.targets {
			normTarget := normalizeDashVariants(target)
			lowerTarget := strings.ToLower(normTarget)
			if len(lowerCleanDash) < len(lowerTarget) && strings.HasPrefix(lowerTarget, lowerCleanDash) {
				return ""
			}
			for _, wrapper := range echoWrappers {
				wDash := normalizeDashVariants(wrapper)
				if strings.HasPrefix(lowerCleanDash, strings.ToLower(wDash)) {
					bodyDash := lowerCleanDash[len(wDash):]
					if len(bodyDash) < len(lowerTarget) && strings.HasPrefix(lowerTarget, bodyDash) {
						return ""
					}
				} else if len(lowerCleanDash) < len(wDash) && strings.HasPrefix(strings.ToLower(wDash), lowerCleanDash) {
					return ""
				}
			}
		}

		// Confirmed not an echo: emit what was held.
		f.phase = 2
		f.held.Reset()
		return clean
	}

	return f.skipResidue(f.held.String())
}

// skipResidue consumes the closing wrapper and separator punctuation that separates the
// echo from the real text, and emits from the first character of that text.
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
	for len(s) > 0 && strings.ContainsRune(".:-,#", rune(s[0])) {
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
