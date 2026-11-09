package render

import (
	"fmt"
	"strings"

	"loop/pkg/ui/style"
)

// FormatPromptSeparator formats the horizontal line with reasoning status and git branch above the prompt.
func FormatPromptSeparator(showThinking bool, reasoningEffort string, theme style.UITheme, width int, gitBranch ...string) string {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	branchStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	thinkingText := "off"
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	if effort != "off" && effort != "none" && effort != "" {
		thinkingText = effort
	}

	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
	statusLen := style.CellWidth(statusPart)
	prefix := "─── prompt "
	available := width - 1
	if available <= 0 {
		return "\n"
	}
	if available <= style.CellWidth(prefix) {
		return borderStyle.Render(style.TruncateCells(prefix, available)) + "\n"
	}

	if style.CellWidth(prefix)+statusLen >= available {
		return borderStyle.Render(style.TruncateCells(prefix+statusPart, available)) + "\n"
	}

	var branchStyled string
	var branchLen int
	if len(gitBranch) > 0 && strings.TrimSpace(gitBranch[0]) != "" {
		b := strings.TrimSpace(gitBranch[0])
		if width < 70 && len(b) > 14 {
			b = style.TruncateCells(b, 14)
		}
		branchRaw := "── ⎇ " + b + " "
		branchLen = style.CellWidth(branchRaw)
		if available > style.CellWidth(prefix)+branchLen+statusLen+4 {
			branchStyled = borderStyle.Render("── ") + branchStyle.Render("⎇ "+b) + " "
		} else {
			branchLen = 0
		}
	}

	used := style.CellWidth(prefix) + branchLen + statusLen
	if available <= used {
		branchStyled = ""
		branchLen = 0
		used = style.CellWidth(prefix) + statusLen
	}

	dashCount := max(1, available-used)
	dashes := borderStyle.Render(strings.Repeat("─", dashCount))
	return borderStyle.Render(prefix) + branchStyled + dashes + statusStyle.Render(statusPart) + "\n"
}

// FormatStaticStatsContent combines spinner and stats text with styling.
func FormatStaticStatsContent(spinnerFrame string, statsText string, theme style.UITheme) string {
	if spinnerFrame == "" && statsText == "" {
		return ""
	}
	if spinnerFrame != "" {
		displayFrame := spinnerFrame
		if !strings.Contains(spinnerFrame, "\x1b[") {
			displayFrame = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(spinnerFrame)
		}
		if statsText != "" {
			return fmt.Sprintf("%s %s", statsText, displayFrame)
		}
		return displayFrame
	}
	return statsText
}
