package render

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if toolName == "bash" {
		fmt.Fprintln(w, FormatBashCommandLine(symbol, pathVal, theme))
		return
	}
	title := FormatToolTitle(symbol, toolName, pathVal, theme)
	fmt.Fprintln(w, title)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme style.UITheme, toolName string, argsJSON string, bodyWasStreamed bool) {
	if !isError && IsWriteLikeTool(toolName) {
		renderWriteSummary(w, theme, toolName, argsJSON, output)
		if bodyWasStreamed {
			return
		}
	}
	if !isError && IsCommandLikeTool(toolName) && bodyWasStreamed {
		return
	}
	status := ToolStatusSuccess
	if isError {
		status = ToolStatusError
	}
	finalDot := RenderToolSymbol(toolName, status, theme)
	inlineSuccessfulOutput := !isError

	if !inlineSuccessfulOutput {
		fmt.Fprintln(w)
	}

	borderColor := theme.Success
	title := fmt.Sprintf("  %s Output", finalDot)
	if isError {
		borderColor = theme.Error
		title = fmt.Sprintf("  %s Error", finalDot)
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
		collapsedMsg := fmt.Sprintf("  ... [%d lines collapsed. Press Ctrl+O or type /expand to view output] ...", collapsedCount)
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

	checkStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
	pathStyle := style.NewStyle().Foreground(theme.Text)
	sizeStyle := style.NewStyle().Foreground(theme.TextMuted)

	if sizeDesc != "" {
		fmt.Fprintf(w, "%s %s %s\n",
			checkStyle.Render("✓"),
			pathStyle.Render(absPath),
			sizeStyle.Render(fmt.Sprintf("(%s)", sizeDesc)),
		)
	} else {
		fmt.Fprintf(w, "%s %s\n",
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

