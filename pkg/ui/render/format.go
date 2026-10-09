package render

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func IsWriteLikeTool(toolName string) bool {
	return toolName == "write" || strings.Contains(toolName, "write") || strings.Contains(toolName, "replace")
}

func IsCommandLikeTool(toolName string) bool {
	lower := strings.ToLower(toolName)
	return lower == "bash" || lower == "exec" || lower == "sh" || strings.Contains(lower, "command") || strings.Contains(lower, "shell") || strings.Contains(lower, "run")
}

func GetToolGlyph(toolName string) string {
	lower := strings.ToLower(toolName)
	switch {
	case lower == "read" || strings.Contains(lower, "read") || strings.Contains(lower, "view"):
		return "◈"
	case lower == "grep" || strings.Contains(lower, "grep") || strings.Contains(lower, "search"):
		return "⌕"
	case lower == "write" || strings.Contains(lower, "write"):
		return "◆"
	case lower == "edit" || strings.Contains(lower, "edit") || strings.Contains(lower, "replace") || strings.Contains(lower, "patch"):
		return "✎"
	case lower == "bash" || lower == "exec" || lower == "sh" || strings.Contains(lower, "command"):
		return "$"
	case lower == "load_skill" || strings.Contains(lower, "skill"):
		return "✦"
	case lower == "create_subagent" || lower == "spawn_subagent" || strings.HasPrefix(lower, "subagent__") || strings.HasPrefix(lower, "swarm_") || lower == "delegate":
		return "❖"
	case lower == "task_kill" || lower == "kill":
		return "✖"
	case lower == "task_status" || lower == "task_list" || lower == "ps":
		return "≡"
	default:
		return "●"
	}
}

func RenderToolSymbol(toolName string, status ToolRenderStatus, theme style.UITheme) string {
	glyph := GetToolGlyph(toolName)
	if status == ToolStatusError {
		return style.NewStyle().Foreground(theme.Error).Bold(true).Render("!")
	}

	var color color.Color
	if status == ToolStatusSuccess {
		color = theme.Success
	} else {
		lower := strings.ToLower(toolName)
		switch {
		case lower == "read" || strings.Contains(lower, "read") || strings.Contains(lower, "view"):
			color = theme.Primary
		case lower == "write" || strings.Contains(lower, "write"):
			color = theme.Highlight
		case lower == "edit" || strings.Contains(lower, "edit") || strings.Contains(lower, "replace"):
			color = theme.Highlight
		case lower == "create_subagent" || lower == "spawn_subagent" || strings.HasPrefix(lower, "subagent__") || strings.HasPrefix(lower, "swarm_") || lower == "delegate":
			color = theme.Secondary
		case lower == "load_skill" || strings.Contains(lower, "skill"):
			color = theme.Highlight
		default:
			color = theme.Primary
		}
	}
	return style.NewStyle().Foreground(color).Render(glyph)
}

func FormatBashCommandLine(symbol string, command string, theme style.UITheme) string {
	promptStyle := style.NewStyle().Foreground(theme.Success)
	cmdStyle := style.NewStyle().Foreground(theme.Text)

	command = strings.ReplaceAll(command, "\r\n", "\n")
	command = strings.ReplaceAll(command, "\r", "\n")
	command = strings.TrimRight(command, "\n")
	if strings.TrimSpace(command) == "" {
		return promptStyle.Render("$")
	}

	lines := strings.Split(command, "\n")
	var formatted []string
	for idx, line := range lines {
		cleaned := strings.Map(func(r rune) rune {
			if (r < 32 && r != '\t') || r == 127 {
				return -1
			}
			return r
		}, line)

		if idx == 0 {
			formatted = append(formatted, fmt.Sprintf("%s %s", promptStyle.Render("$"), cmdStyle.Render(cleaned)))
		} else {
			formatted = append(formatted, fmt.Sprintf("  %s", cmdStyle.Render(cleaned)))
		}
	}

	return strings.Join(formatted, "\n")
}

func GetActionStyle(toolName string, theme style.UITheme) style.Style {
	lower := strings.ToLower(toolName)
	switch {
	case lower == "task_kill" || lower == "kill":
		return style.NewStyle().Foreground(theme.Error).Bold(true)
	case lower == "task_status" || lower == "task_list" || lower == "ps":
		return style.NewStyle().Foreground(theme.TextMuted).Bold(true)
	case lower == "create_subagent" || lower == "spawn_subagent" || strings.HasPrefix(lower, "subagent__") || strings.HasPrefix(lower, "swarm_") || lower == "delegate":
		return style.NewStyle().Foreground(theme.Secondary).Bold(true)
	case lower == "edit" || strings.Contains(lower, "edit") || strings.Contains(lower, "replace"):
		return style.NewStyle().Foreground(theme.Highlight).Bold(true)
	case lower == "write" || strings.Contains(lower, "write"):
		return style.NewStyle().Foreground(theme.Success).Bold(true)
	default:
		return style.NewStyle().Foreground(theme.Primary).Bold(true)
	}
}

func FormatToolTitle(symbol string, toolName string, path string, theme style.UITheme) string {
	toolStyle := GetActionStyle(toolName, theme)
	pathStyle := style.NewStyle().Foreground(theme.Text)

	width, _ := terminal.GetDimensions()
	if width <= 0 {
		width = 80
	}
	targetWidth := width - 2

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
		isNonFilePathTool := toolName == "create_subagent" || toolName == "spawn_subagent" || strings.HasPrefix(toolName, "subagent__") || toolName == "task_status" || toolName == "task_kill" || toolName == "load_skill" || toolName == "bash"
		if toolName != "ls" && !isNonFilePathTool {
			wd, err := os.Getwd()
			if err == nil {
				if rel, err := filepath.Rel(wd, path); err == nil {
					relPath = rel
				}
			}
		}
		symbolLen := utf8.RuneCountInString(style.StripAnsi(symbol))
		maxPathRunes := targetWidth - symbolLen - utf8.RuneCountInString(toolName) - 6
		if maxPathRunes < 8 {
			maxPathRunes = 8
		}
		pathRunes := []rune(relPath)
		if len(pathRunes) > maxPathRunes {
			relPath = string(pathRunes[:maxPathRunes-3]) + "..."
		}
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
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= limit {
			currentLine.WriteByte(' ')
			currentLine.WriteString(word)
		} else {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}
	return lines
}
