package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/render/syntax"
	"loop/pkg/ui/style"
)

func padRightCells(text string, width int) string {
	return style.PadRightCells(text, width)
}

// TailCells returns s unchanged when its display width fits within maxCells;
// otherwise it keeps the trailing grapheme clusters fitting alongside "…".
func TailCells(s string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if style.CellWidth(s) <= maxCells {
		return s
	}
	if maxCells == 1 {
		return "…"
	}
	type graphemeSpan struct {
		text  string
		width int
	}
	var clusters []graphemeSpan
	for i := 0; i < len(s); {
		cluster, cw, sz := style.NextGrapheme(s[i:])
		clusters = append(clusters, graphemeSpan{text: cluster, width: cw})
		i += sz
	}
	budget := maxCells - 1
	used := 0
	start := len(clusters)
	for start > 0 {
		w := clusters[start-1].width
		if used+w > budget {
			break
		}
		used += w
		start--
	}
	var b strings.Builder
	b.WriteString("…")
	for _, g := range clusters[start:] {
		b.WriteString(g.text)
	}
	return b.String()
}

func isReadLike(lower string) bool {
	return lower == "read" || strings.Contains(lower, "read") || strings.Contains(lower, "view")
}

func isSubagentLike(lower string) bool {
	return lower == "create_subagent" || lower == "remove_subagent" || lower == "list_subagents" || lower == "inspect_subagent" || lower == "spawn_subagent" ||
		strings.HasPrefix(lower, "subagent__") || strings.HasPrefix(lower, "swarm_") || lower == "delegate"
}

func isTodoLike(lower string) bool {
	return lower == "todo" || strings.Contains(lower, "todo") || lower == "plan"
}

func isAskLike(lower string) bool {
	return lower == "ask_user" || strings.Contains(lower, "ask")
}

func isSkillLike(lower string) bool {
	return lower == "load_skill" || strings.Contains(lower, "skill")
}

func isTaskStatusLike(lower string) bool {
	return lower == "get_task_status" || lower == "task_list" || lower == "ps"
}

func isTaskKillLike(lower string) bool {
	return lower == "stop_task" || lower == "kill"
}

func IsWriteLikeTool(toolName string) bool {
	toolName = domaintool.NormalizeName(toolName)
	return toolName == "write" || strings.Contains(toolName, "write") || strings.Contains(toolName, "replace")
}

func IsEditLikeTool(toolName string) bool {
	toolName = domaintool.NormalizeName(toolName)
	lower := strings.ToLower(toolName)
	return lower == "edit" || strings.Contains(lower, "edit") || strings.Contains(lower, "patch")
}

func IsCommandLikeTool(toolName string) bool {
	toolName = domaintool.NormalizeName(toolName)
	lower := strings.ToLower(toolName)
	return lower == "bash" || lower == "exec" || lower == "sh" ||
		strings.Contains(lower, "command") || strings.Contains(lower, "shell") || strings.Contains(lower, "run")
}

func GetToolGlyph(toolName string) string {
	toolName = domaintool.NormalizeName(toolName)
	lower := strings.ToLower(toolName)
	switch {
	case isReadLike(lower):
		return "▤"
	case lower == "grep" || strings.Contains(lower, "grep") || strings.Contains(lower, "search"):
		return "⌕"
	case lower == "write" || strings.Contains(lower, "write"):
		return "✚"
	case IsEditLikeTool(lower) || strings.Contains(lower, "replace"):
		return "✐"
	case lower == "bash" || lower == "exec" || lower == "sh" || strings.Contains(lower, "command"):
		return "$"
	case isSkillLike(lower):
		return "✦"
	case isSubagentLike(lower):
		return "❖"
	case isTaskKillLike(lower):
		return "✖"
	case isTaskStatusLike(lower):
		return "⚙"
	case isTodoLike(lower):
		return "≡"
	case isAskLike(lower):
		return "?"
	default:
		return "●"
	}
}

func ThinkingLineCount(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}

func FormatThinkingSummary(theme style.UITheme, seconds float64, active bool, lines int, showMicroseconds bool) string {
	label := "⋮ thinking…"
	if !active {
		label = "⋮ thought"
		if seconds > 0 {
			label += " (" + style.FormatElapsedDuration(time.Duration(seconds*float64(time.Second)), showMicroseconds) + ")"
		}
	}
	unit := "lines"
	if lines == 1 {
		unit = "line"
	}
	label += fmt.Sprintf(" · %d %s hidden", lines, unit)
	if !active {
		label += " · Ctrl+O to expand"
	}
	return style.NewStyle().Foreground(theme.TextMuted).Italic(true).Render(label)
}

func RenderToolSymbol(toolName string, status ToolRenderStatus, theme style.UITheme) string {
	toolName = domaintool.NormalizeName(toolName)
	if status == ToolStatusError {
		return style.NewStyle().Foreground(theme.Error).Bold(true).Render("!")
	}

	c := theme.Primary
	if status == ToolStatusSuccess {
		c = theme.Success
	}
	return style.NewStyle().Foreground(c).Render(GetToolGlyph(toolName))
}

func FormatBashCommandLine(symbol string, command string, theme style.UITheme) string {
	return FormatBashCommandLineStyled(symbol, command, theme, true)
}

func FormatBashCommandLineStyled(symbol string, command string, theme style.UITheme, syntaxColor bool) string {
	prompt := symbol
	if strings.TrimSpace(prompt) == "" {
		prompt = style.NewStyle().Foreground(theme.Primary).Render("$")
	}
	cmdStyle := style.NewStyle().Foreground(theme.Text)

	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	command = strings.TrimRight(command, "\n")
	if strings.TrimSpace(command) == "" {
		return prompt
	}

	lines := strings.Split(command, "\n")
	cleanedLines := make([]string, len(lines))
	for idx, line := range lines {
		cleanedLines[idx] = strings.Map(func(r rune) rune {
			if (r < 32 && r != '\t') || r == 127 {
				return -1
			}
			return r
		}, line)
	}

	var styledLines []string
	if syntaxColor {
		styledLines = syntax.HighlightBashCommandLines(cleanedLines, theme)
	} else {
		styledLines = make([]string, len(cleanedLines))
		for idx, cleaned := range cleanedLines {
			styledLines[idx] = cmdStyle.Render(cleaned)
		}
	}

	formatted := make([]string, len(styledLines))
	for idx, styled := range styledLines {
		if idx == 0 {
			formatted[idx] = fmt.Sprintf("%s %s", prompt, styled)
		} else {
			formatted[idx] = fmt.Sprintf("  %s", styled)
		}
	}

	return strings.Join(formatted, "\n")
}

func GetActionStyle(toolName string, theme style.UITheme) style.Style {
	toolName = domaintool.NormalizeName(toolName)
	lower := strings.ToLower(toolName)
	c := theme.Primary
	if isTaskKillLike(lower) {
		c = theme.Error
	}
	return style.NewStyle().Foreground(c).Bold(true)
}

func FormatToolTitleForWidth(symbol string, toolName string, path string, theme style.UITheme, width int) string {
	toolName = domaintool.NormalizeName(toolName)
	if toolName == "task_state" {
		return symbol + " " + GetActionStyle(toolName, theme).Render("Task progress")
	}
	toolStyle := GetActionStyle(toolName, theme)
	pathStyle := style.NewStyle().Foreground(theme.Text)

	if width <= 0 {
		width = 80
	}
	targetWidth := max(1, width-2)

	path = style.StripAnsi(path)
	path = strings.Join(strings.FieldsFunc(path, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
	path = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, path)

	if path != "" {
		relPath := path
		isNonFilePathTool := toolName == "create_subagent" || toolName == "spawn_subagent" ||
			strings.HasPrefix(toolName, "subagent__") || toolName == "get_task_status" ||
			toolName == "stop_task" || toolName == "load_skill" || toolName == "bash"
		if toolName != "ls" && !isNonFilePathTool {
			if wd, err := os.Getwd(); err == nil {
				if rel, err := filepath.Rel(wd, path); err == nil {
					relPath = rel
				}
			}
		}
		symbolLen := style.CellWidth(symbol)
		maxPathCells := max(targetWidth-symbolLen-style.CellWidth(toolName)-6, 8)
		relPath = style.TruncateCells(relPath, maxPathCells)
		if symbol != "" {
			return fmt.Sprintf("%s %s %s", symbol, toolStyle.Render(toolName), pathStyle.Render(relPath))
		}
		return fmt.Sprintf("%s %s", toolStyle.Render(toolName), pathStyle.Render(relPath))
	}

	if symbol != "" {
		return fmt.Sprintf("%s %s", symbol, toolStyle.Render(toolName))
	}
	return toolStyle.Render(toolName)
}

func WrapText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return []string{""}
	}
	return style.WrapAnsiLine(collapsed, limit)
}
