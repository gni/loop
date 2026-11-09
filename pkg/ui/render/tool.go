package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"loop/pkg/domain/task"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v3/quick"

	filetool "loop/pkg/agent/tool/file"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

type ToolRenderStatus uint8

const (
	ToolStatusPending ToolRenderStatus = iota
	ToolStatusSuccess
	ToolStatusError
)

func RepairArgsJSON(js string) string {
	return domaintool.RepairJSON(js)
}

func RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, argsJSON string) {
	toolName = domaintool.NormalizeName(toolName)
	symbol := RenderToolSymbol(toolName, ToolStatusPending, theme)
	pathVal := ExtractToolTarget(toolName, argsJSON)
	if pathVal == "" && IsCommandLikeTool(toolName) && strings.TrimSpace(argsJSON) != "" && strings.TrimSpace(argsJSON) != "{}" {
		pathVal = strings.TrimSpace(argsJSON)
	}
	if toolName == "bash" || IsCommandLikeTool(toolName) {
		fmt.Fprintln(w, FormatBashCommandLine(symbol, pathVal, theme))
		return
	}
	fmt.Fprintln(w, FormatToolTitle(symbol, toolName, pathVal, theme))
}

func isCodeContentTool(toolName string) bool {
	toolName = domaintool.NormalizeName(toolName)
	if strings.Contains(toolName, "edit") || strings.Contains(toolName, "replace") {
		return false
	}
	return toolName == "read" || toolName == "write" ||
		strings.Contains(toolName, "read") ||
		strings.Contains(toolName, "write") ||
		strings.Contains(toolName, "view") ||
		strings.Contains(toolName, "content") ||
		strings.Contains(toolName, "file")
}

func highlightToolBody(body, toolName, argsJSON string, isJSON, bodyWasStreamed bool, theme style.UITheme) string {
	toolName = domaintool.NormalizeName(toolName)
	lang := "plaintext"
	if isJSON {
		lang = "json"
	} else if toolName == "bash" {
		lang = ShellOutputLanguage(ExtractToolTarget(toolName, argsJSON), body)
	} else {
		if filePath := ExtractToolTarget(toolName, argsJSON); filePath != "" {
			lang = filePath
		}

		if IsWriteLikeTool(toolName) && !bodyWasStreamed && argsJSON != "" {
			if extracted, ok := filetool.ExtractFileContent(argsJSON); ok {
				body = extracted
			} else {
				body = argsJSON
				lang = "json"
			}
		}
	}

	lang = ResolveSyntaxLanguage(lang, body)
	chromaStyle := theme.ChromaStyle
	if chromaStyle == "" || chromaStyle == "bw" {
		chromaStyle = "friendly"
	}
	var codeBuf bytes.Buffer
	err := quick.Highlight(&codeBuf, body, lang, "terminal256", chromaStyle)
	if err != nil {
		codeBuf.Reset()
		err = quick.Highlight(&codeBuf, body, lang, "terminal16", chromaStyle)
	}
	if err == nil && codeBuf.Len() > 0 {
		return codeBuf.String()
	}
	return body
}

// trimRenderedToolBody removes extra terminal newlines at the end of a
// syntax-highlighted write preview without dropping trailing ANSI SGR resets.
// Chroma can place reset codes *after* a newline, so TrimRight alone misses it.
func trimRenderedToolBody(body string) string {
	var sgrSuffix string
	for {
		body = strings.TrimRight(body, "\r\n")
		start := strings.LastIndex(body, "\x1b[")
		if start < 0 || !strings.HasSuffix(body, "m") {
			break
		}
		sequence := body[start:]
		if len(sequence) < 3 {
			break
		}
		valid := true
		for i := 2; i < len(sequence)-1; i++ {
			if c := sequence[i]; (c < '0' || c > '9') && c != ';' {
				valid = false
				break
			}
		}
		if !valid {
			break
		}
		sgrSuffix = sequence + sgrSuffix
		body = body[:start]
	}
	return body + sgrSuffix
}

func renderCollapsedNotice(w io.Writer, lineCount int, theme style.UITheme) {
	collapseMarker := style.NewStyle().Foreground(theme.Border).Render("⋮  ")
	unit := "lines"
	if lineCount == 1 {
		unit = "line"
	}
	msg := fmt.Sprintf("%s%d %s hidden · Ctrl+O to expand", collapseMarker, lineCount, unit)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(msg))
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme style.UITheme, toolName string, argsJSON string, bodyWasStreamed bool) {
	toolName = domaintool.NormalizeName(toolName)
	if !isError {
		if toolName == "task_state" {
			if !task.ShowProgressNotice(output, argsJSON) {
				return
			}
			if renderTaskState(w, output, argsJSON, collapse, theme) {
				return
			}
		}
		if toolName == "todo" && bodyWasStreamed {
			// Task descriptions were already shown during generation. Keep
			// the verified status counts without printing the full list again.
			if summary, _, ok := strings.Cut(output, "\n"); ok && strings.TrimSpace(summary) != "" {
				fmt.Fprintln(w, style.NewStyle().Foreground(theme.Success).Render("⎿  "+summary))
				return
			}
		}
		// Never suppress the write completion line just because its code was
		// streamed. Emit only the saved-file summary, not the raw tool output.
		if toolName == "write" {
			if !bodyWasStreamed && !collapse {
				if content, ok := filetool.ExtractFileContent(argsJSON); ok && content != "" {
					// Match live streaming: the summary follows the last content
					// line directly, even if Chroma puts ANSI resets after
					// trailing newlines. A blank line belongs between calls,
					// not between a write body and its verified result.
					body := highlightToolBody(content, toolName, argsJSON, false, false, theme)
					fmt.Fprintln(w, trimRenderedToolBody(body))
				}
			}
			renderWriteSummary(w, theme, toolName, argsJSON, output)
			return
		}
		if (IsWriteLikeTool(toolName) || IsCommandLikeTool(toolName)) && bodyWasStreamed {
			return
		}
		if IsWriteLikeTool(toolName) {
			renderWriteSummary(w, theme, toolName, argsJSON, output)
		}
		if toolName == "get_task_status" || toolName == "stop_task" {
			renderTaskSummary(w, theme, toolName, argsJSON, output, collapse)
			return
		}
	}

	if isError && toolName == "bash" && bodyWasStreamed {
		finalDot := RenderToolSymbol(toolName, ToolStatusError, theme)
		message := "Command failed"
		if first, ok := strings.CutPrefix(output, "[Command Failed: "); ok {
			if detail, _, found := strings.Cut(first, "]"); found {
				message += ": " + detail
			}
		}
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render(fmt.Sprintf("⎿  %s %s", finalDot, message)))
		fmt.Fprintln(w, "   "+FailureAdvice(output))
		return
	}

	if isError {
		renderActionableFailure(w, output, collapse, theme)
		return
	}
	body := output
	isJSON := false
	if !isError {
		if toolName == "ask_user" {
			body = askUserSelectionSummary(body)
		}
		trimmedBody := strings.TrimSpace(body)
		if (strings.HasPrefix(trimmedBody, "{") && strings.HasSuffix(trimmedBody, "}")) || (strings.HasPrefix(trimmedBody, "[") && strings.HasSuffix(trimmedBody, "]")) {
			var temp interface{}
			if json.Unmarshal([]byte(trimmedBody), &temp) == nil {
				if collapse && toolName != "bash" && !isCodeContentTool(toolName) {
					renderStructuredSummary(w, temp, theme)
					return
				}
				if pretty, err := json.MarshalIndent(temp, "", "  "); err == nil {
					body = string(pretty)
					isJSON = true
				}
			}
		}
		if toolName == "grep" && !strings.Contains(body, "\x1b[") {
			body = highlightGrepBody(body, theme)
		} else if (isCodeContentTool(toolName) || isJSON || (toolName == "bash" && ShellOutputLanguage(ExtractToolTarget(toolName, argsJSON), body) != "plaintext")) && !strings.Contains(body, "\x1b[") {
			body = highlightToolBody(body, toolName, argsJSON, isJSON, bodyWasStreamed, theme)
		}
	}

	body = strings.TrimRight(body, "\r\n")
	lines := strings.Split(body, "\n")

	if collapse && !isError && len(lines) > 0 {
		renderCollapsedNotice(w, len(lines), theme)
	} else {
		bodyStyle := style.NewStyle().Foreground(theme.Text)
		for _, line := range lines {
			if strings.Contains(line, "\x1b[") {
				fmt.Fprintln(w, line)
			} else {
				fmt.Fprintln(w, bodyStyle.Render(line))
			}
		}
	}
	if !isError && IsEditLikeTool(toolName) {
		renderEditSummary(w, theme, toolName, argsJSON, output)
	}
}

// The complete question and answer remain in model history. The question is
// already visible in the menu or tool heading, so display only the answer here.
func askUserSelectionSummary(output string) string {
	rest, ok := strings.CutPrefix(output, "User response to question ")
	if !ok {
		return output
	}
	question, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return output
	}
	answer, ok := strings.CutPrefix(rest[len(question):], ": Selected ")
	if !ok {
		return output
	}
	if _, err := strconv.Unquote(answer); err != nil {
		return output
	}
	return "⎿  Selected " + answer
}

func resolveAbsTargetPath(target string) string {
	if target == "" || filepath.IsAbs(target) {
		return target
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Join(cwd, target)
	}
	return target
}

func renderWriteSummary(w io.Writer, theme style.UITheme, toolName string, argsJSON string, output string) {
	toolName = domaintool.NormalizeName(toolName)
	target := ExtractToolTarget(toolName, argsJSON)
	sizeBytes := int64(-1)
	firstLine, _, _ := strings.Cut(output, "\n")
	if message, ok := strings.CutPrefix(firstLine, "Successfully wrote "); ok {
		if count, savedPath, ok := strings.Cut(message, " bytes to "); ok {
			if n, err := strconv.ParseInt(count, 10, 64); err == nil && n >= 0 {
				sizeBytes = n
				if strings.TrimSpace(savedPath) != "" {
					target = strings.TrimSpace(savedPath)
				}
			}
		}
	}
	if target == "" {
		return
	}

	absPath := resolveAbsTargetPath(target)
	if sizeBytes < 0 {
		if fi, err := os.Stat(absPath); err == nil && fi.Mode().IsRegular() {
			sizeBytes = fi.Size()
		} else if contents, ok := filetool.ExtractFileContent(argsJSON); ok {
			sizeBytes = int64(len(contents))
		}
	}

	branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
	pathStyle := style.NewStyle().Foreground(theme.Text)
	if sizeBytes >= 0 {
		sizeStyle := style.NewStyle().Foreground(theme.TextMuted)
		fmt.Fprintf(w, "%s%s %s\n", branchStyle,
			pathStyle.Render(absPath), sizeStyle.Render(fmt.Sprintf("(%d bytes)", sizeBytes)))
		return
	}
	fmt.Fprintf(w, "%s%s\n", branchStyle, pathStyle.Render(absPath))
}

// CountDiffLines counts the number of added and deleted lines from diff output.
func CountDiffLines(diffText string) (added int, deleted int) {
	for _, rawLine := range strings.Split(diffText, "\n") {
		trimmed := strings.TrimSpace(style.StripAnsi(rawLine))
		if trimmed == "" || trimmed == "..." {
			continue
		}
		if strings.Contains(rawLine, "\x1b[32m") {
			added++
			continue
		}
		if strings.Contains(rawLine, "\x1b[31m") {
			deleted++
			continue
		}
		if fields := strings.Fields(trimmed); len(fields) >= 2 {
			switch fields[1] {
			case "+":
				added++
				continue
			case "-":
				deleted++
				continue
			}
		}
		if strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++") {
			added++
		} else if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---") {
			deleted++
		}
	}
	return added, deleted
}

// FormatDiffStats colors added and deleted line counts independently.
func FormatDiffStats(added, deleted int, theme style.UITheme) string {
	addedStyle := style.NewStyle().Foreground(theme.Success)
	deletedStyle := style.NewStyle().Foreground(theme.Error)
	return fmt.Sprintf("%s / %s lines",
		addedStyle.Render(fmt.Sprintf("+%d", added)),
		deletedStyle.Render(fmt.Sprintf("-%d", deleted)),
	)
}

func renderEditSummary(w io.Writer, theme style.UITheme, toolName string, argsJSON string, output string) {
	toolName = domaintool.NormalizeName(toolName)
	target := ExtractToolTarget(toolName, argsJSON)
	if target == "" {
		return
	}

	absPath := resolveAbsTargetPath(target)
	added, deleted := CountDiffLines(output)
	statsDesc := FormatDiffStats(added, deleted, theme)

	branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
	pathStyle := style.NewStyle().Foreground(theme.Text)

	if statsDesc != "" {
		fmt.Fprintf(w, "%s%s (%s)\n", branchStyle, pathStyle.Render(absPath), statsDesc)
	} else {
		fmt.Fprintf(w, "%s%s\n", branchStyle, pathStyle.Render(absPath))
	}
}

func renderTaskSummary(w io.Writer, theme style.UITheme, toolName, argsJSON, output string, collapse bool) {
	id := ExtractToolTarget(toolName, argsJSON)
	if id == "" {
		fields := strings.Fields(output)
		if len(fields) > 1 && fields[0] == "Task" {
			id = fields[1]
		}
	}
	if id == "" {
		id = "task"
	}
	status := "running"
	lower := strings.ToLower(output)
	if toolName == "stop_task" {
		status = "killed"
	} else {
		for _, candidate := range []string{"completed", "killed", "failed"} {
			if strings.Contains(lower, "currently: "+candidate) || strings.Contains(lower, "status: "+candidate) {
				status = candidate
				break
			}
		}
	}
	command := ""
	first, _, _ := strings.Cut(output, "\n")
	if start := strings.Index(first, "("); start >= 0 {
		if end := strings.LastIndex(first, ")"); end > start {
			command = first[start+1 : end]
		}
	}
	body := ""
	if _, tail, ok := strings.Cut(output, "Output:\n"); ok {
		body = tail
	}
	RenderBackgroundTask(w, id, command, status, body, collapse, theme)
}
