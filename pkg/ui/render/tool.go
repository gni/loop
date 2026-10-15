package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"

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
	symbol := RenderToolSymbol(toolName, ToolStatusPending, theme)
	pathVal := ExtractToolTarget(toolName, argsJSON)
	if pathVal == "" && IsCommandLikeTool(toolName) && strings.TrimSpace(argsJSON) != "" && strings.TrimSpace(argsJSON) != "{}" {
		pathVal = strings.TrimSpace(argsJSON)
	}
	if toolName == "bash" || IsCommandLikeTool(toolName) {
		fmt.Fprintln(w, FormatBashCommandLine(symbol, pathVal, theme))
		return
	}
	title := FormatToolTitle(symbol, toolName, pathVal, theme)
	fmt.Fprintln(w, title)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme style.UITheme, toolName string, argsJSON string, bodyWasStreamed bool) {
	if !isError && IsWriteLikeTool(toolName) && bodyWasStreamed {
		return
	}
	if !isError && IsWriteLikeTool(toolName) {
		renderWriteSummary(w, theme, toolName, argsJSON, output)
	}
	if !isError && IsEditLikeTool(toolName) {
		renderEditSummary(w, theme, toolName, argsJSON, output)
	}
	if !isError && IsCommandLikeTool(toolName) && bodyWasStreamed {
		return
	}
	if !isError && (toolName == "task_status" || toolName == "task_kill") {
		renderTaskSummary(w, theme, toolName, argsJSON, output, collapse)
		return
	}
	status := ToolStatusSuccess
	if isError {
		status = ToolStatusError
	}
	finalDot := RenderToolSymbol(toolName, status, theme)
	inlineSuccessfulOutput := !isError

	borderColor := theme.Success
	title := fmt.Sprintf("⎿  %s Output", finalDot)
	if isError {
		borderColor = theme.Error
		title = fmt.Sprintf("⎿  %s Error", finalDot)
	}

	titleStyle := style.NewStyle().
		Foreground(borderColor).
		Bold(true)

	bodyStyle := style.NewStyle().
		Foreground(theme.Text)

	body := output
	isJSON := false
	if !isError {
		trimmedBody := strings.TrimSpace(body)
		if (strings.HasPrefix(trimmedBody, "{") && strings.HasSuffix(trimmedBody, "}")) || (strings.HasPrefix(trimmedBody, "[") && strings.HasSuffix(trimmedBody, "]")) {
			var temp interface{}
			if json.Unmarshal([]byte(trimmedBody), &temp) == nil {
				if pretty, err := json.MarshalIndent(temp, "", "  "); err == nil {
					body = string(pretty)
					isJSON = true
				}
			}
		}
	}

	isCodeTool := (toolName == "read" || toolName == "write" ||
		strings.Contains(toolName, "read") ||
		strings.Contains(toolName, "write") ||
		strings.Contains(toolName, "view") ||
		strings.Contains(toolName, "content") ||
		strings.Contains(toolName, "file")) &&
		!strings.Contains(toolName, "edit") &&
		!strings.Contains(toolName, "replace")

	if !isError && (isCodeTool || isJSON) && !strings.Contains(body, "\x1b[") {
		lang := "plaintext"
		if isJSON {
			lang = "json"
		} else {
			filePath := ExtractToolTarget(toolName, argsJSON)
			if filePath != "" {
				ext := filepath.Ext(filePath)
				if len(ext) > 1 {
					lang = ext[1:]
				}
			}

			if IsWriteLikeTool(toolName) && !bodyWasStreamed {
				var args struct {
					Content            string `json:"content"`
					WriteContent       string `json:"write_content"`
					CodeContent        string `json:"CodeContent"`
					ReplacementContent string `json:"ReplacementContent"`
					Text               string `json:"text"`
					Body               string `json:"body"`
				}
				if argsJSON != "" {
					err := json.Unmarshal([]byte(argsJSON), &args)
					if err != nil {
						body = argsJSON
						lang = "json"
					} else {
						body = domaintool.FirstNonEmpty(
							args.CodeContent,
							args.ReplacementContent,
							args.Content,
							args.WriteContent,
							args.Text,
							args.Body,
						)
					}
				}
			}
		}

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
			body = codeBuf.String()
		}
	}

	if !inlineSuccessfulOutput {
		fmt.Fprintln(w, titleStyle.Render(title))
	}

	body = strings.TrimRight(body, "\r\n")
	lines := strings.Split(body, "\n")
	printLine := func(line string) {
		var renderedLine string
		if strings.Contains(line, "\x1b[") {
			renderedLine = line
		} else {
			renderedLine = bodyStyle.Render(line)
		}
		fmt.Fprintln(w, renderedLine)
	}

	if collapse && !isError && len(lines) > 0 {
		collapsedCount := len(lines)
		branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
		collapsedMsg := fmt.Sprintf("%s... [%d lines collapsed. Press Ctrl+O or type /expand to view output] ...", branchStyle, collapsedCount)
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(collapsedMsg))
	} else {
		for _, line := range lines {
			printLine(line)
		}
	}
	fmt.Fprintln(w)
}

func renderWriteSummary(w io.Writer, theme style.UITheme, toolName string, argsJSON string, output string) {
	target := ExtractToolTarget(toolName, argsJSON)
	if target == "" {
		if idx := strings.Index(output, " to "); idx != -1 {
			target = strings.TrimSpace(output[idx+4:])
		}
	}
	if target == "" {
		return
	}

	absPath := target
	if !filepath.IsAbs(absPath) {
		if cwd, err := os.Getwd(); err == nil {
			absPath = filepath.Join(cwd, target)
		}
	}

	var sizeBytes int64 = -1
	var lineCount int = -1
	if fi, err := os.Stat(absPath); err == nil && !fi.IsDir() {
		sizeBytes = fi.Size()
		if data, err := os.ReadFile(absPath); err == nil {
			lineCount = bytes.Count(data, []byte{'\n'})
			if len(data) > 0 && !bytes.HasSuffix(data, []byte{'\n'}) {
				lineCount++
			}
		}
	}

	sizeDesc := ""
	if sizeBytes >= 0 {
		formattedSize := formatByteSize(sizeBytes)
		if lineCount >= 0 {
			sizeDesc = fmt.Sprintf("%s, %d lines", formattedSize, lineCount)
		} else {
			sizeDesc = formattedSize
		}
	} else if len(output) > 0 {
		sizeDesc = output
	}

	branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
	checkStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
	pathStyle := style.NewStyle().Foreground(theme.Text)
	sizeStyle := style.NewStyle().Foreground(theme.TextMuted)

	if sizeDesc != "" {
		fmt.Fprintf(w, "%s%s %s %s\n",
			branchStyle,
			checkStyle.Render("✓"),
			pathStyle.Render(absPath),
			sizeStyle.Render(fmt.Sprintf("(%s)", sizeDesc)),
		)
	} else {
		fmt.Fprintf(w, "%s%s %s\n",
			branchStyle,
			checkStyle.Render("✓"),
			pathStyle.Render(absPath),
		)
	}
}

func formatByteSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
}

// CountDiffLines counts the number of added and deleted lines from diff output.
func CountDiffLines(diffText string) (added int, deleted int) {
	lines := strings.Split(diffText, "\n")
	for _, rawLine := range lines {
		stripped := style.StripAnsi(rawLine)
		trimmed := strings.TrimSpace(stripped)
		if trimmed == "" || trimmed == "..." {
			continue
		}
		// Check for ANSI color markers from GenerateDisplayDiff:
		// \x1b[32m is green (diffInsert), \x1b[31m is red (diffDelete)
		if strings.Contains(rawLine, "\x1b[32m") {
			added++
			continue
		}
		if strings.Contains(rawLine, "\x1b[31m") {
			deleted++
			continue
		}
		// Fallback for stripped or non-colored diffs:
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			if fields[1] == "+" {
				added++
				continue
			} else if fields[1] == "-" {
				deleted++
				continue
			}
		}
		// Fallback for standard unified diff format:
		if strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++") {
			added++
		} else if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---") {
			deleted++
		}
	}
	return added, deleted
}

// FormatDiffStats formats the line additions in green and deletions in red.
func FormatDiffStats(added, deleted int, theme style.UITheme) string {
	greenStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
	redStyle := style.NewStyle().Foreground(theme.Error).Bold(true)

	return fmt.Sprintf("%s / %s lines", greenStyle.Render(fmt.Sprintf("+%d", added)), redStyle.Render(fmt.Sprintf("-%d", deleted)))
}

func renderEditSummary(w io.Writer, theme style.UITheme, toolName string, argsJSON string, output string) {
	target := ExtractToolTarget(toolName, argsJSON)
	if target == "" {
		return
	}

	absPath := target
	if !filepath.IsAbs(absPath) {
		if cwd, err := os.Getwd(); err == nil {
			absPath = filepath.Join(cwd, target)
		}
	}

	added, deleted := CountDiffLines(output)
	statsDesc := FormatDiffStats(added, deleted, theme)

	branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
	pathStyle := style.NewStyle().Foreground(theme.Text)

	if statsDesc != "" {
		fmt.Fprintf(w, "%s%s (%s)\n",
			branchStyle,
			pathStyle.Render(absPath),
			statsDesc,
		)
	} else {
		fmt.Fprintf(w, "%s%s\n",
			branchStyle,
			pathStyle.Render(absPath),
		)
	}
}

func renderTaskSummary(w io.Writer, theme style.UITheme, toolName string, argsJSON string, output string, collapse bool) {
	branchStyle := style.NewStyle().Foreground(theme.Border).Render("⎿  ")
	taskID := ExtractToolTarget(toolName, argsJSON)
	if taskID == "" {
		fields := strings.Fields(output)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Task") {
			taskID = fields[1]
		}
	}
	if taskID == "" {
		taskID = "task"
	}

	if toolName == "task_kill" {
		idStyle := style.NewStyle().Foreground(theme.Text).Bold(true)
		statusStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s%s: %s\n\n", branchStyle, idStyle.Render(taskID), statusStyle.Render("terminated"))
		return
	}

	statusStr := "running"
	lower := strings.ToLower(output)
	if strings.Contains(lower, "currently: completed") || strings.Contains(lower, "status: completed") || strings.Contains(lower, "status: done") {
		statusStr = "completed"
	} else if strings.Contains(lower, "currently: killed") || strings.Contains(lower, "status: killed") {
		statusStr = "killed"
	} else if strings.Contains(lower, "currently: failed") || strings.Contains(lower, "status: failed") {
		statusStr = "failed"
	} else if strings.Contains(lower, "currently: running") || strings.Contains(lower, "status: running") {
		statusStr = "running"
	}

	var statusColor color.Color
	switch statusStr {
	case "running":
		statusColor = theme.Success
	case "completed":
		statusColor = theme.Primary
	case "killed", "failed":
		statusColor = theme.Error
	default:
		statusColor = theme.TextMuted
	}

	idStyle := style.NewStyle().Foreground(theme.Text).Bold(true)
	statusStyle := style.NewStyle().Foreground(statusColor).Bold(true)

	cmdDesc := ""
	firstLine := strings.SplitN(output, "\n", 2)[0]
	if openParen := strings.Index(firstLine, "("); openParen != -1 {
		if closeParen := strings.LastIndex(firstLine, ")"); closeParen != -1 && closeParen > openParen {
			rawCmd := firstLine[openParen+1 : closeParen]
			if len(rawCmd) > 40 {
				rawCmd = rawCmd[:37] + "..."
			}
			cmdDesc = fmt.Sprintf(" (%s)", rawCmd)
		}
	}

	cmdStyle := style.NewStyle().Foreground(theme.TextMuted)
	fmt.Fprintf(w, "%s%s: %s%s\n",
		branchStyle,
		idStyle.Render(taskID),
		statusStyle.Render(statusStr),
		cmdStyle.Render(cmdDesc),
	)

	var outBody string
	if idx := strings.Index(output, "Output:\n"); idx != -1 {
		outBody = strings.TrimSpace(output[idx+8:])
	} else if idx := strings.Index(output, "\n\n"); idx != -1 {
		outBody = strings.TrimSpace(output[idx+2:])
	}
	if outBody != "" && outBody != "(no output)" {
		lines := strings.Split(outBody, "\n")
		if collapse && len(lines) > 5 {
			collapsedMsg := fmt.Sprintf("%s... [%d lines collapsed. Press Ctrl+O or type /expand to view output] ...", branchStyle, len(lines))
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(collapsedMsg))
		} else {
			bodyStyle := style.NewStyle().Foreground(theme.Text)
			for _, line := range lines {
				fmt.Fprintf(w, "   %s\n", bodyStyle.Render(line))
			}
		}
	}
	fmt.Fprintln(w)
}

