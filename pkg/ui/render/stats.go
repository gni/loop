package render

import (
	"fmt"
	"strings"

	"loop/pkg/ui/style"
)

func formatPromptSeparatorLine(showThinking bool, reasoningEffort string, theme style.UITheme, width int, gitBranch ...string) string {
	available := width - 1
	if available <= 0 {
		return ""
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	branchStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	// Optional right-hand reasoning badge (" ∴ low", " ∴ med", " ∴ high", " ∴ max", " ∴ off").
	// Omitted completely when reasoningEffort is "hidden" (show_reasoning_badge = false).
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	showBadge := effort != "hidden"
	var statusStyled string
	var statusLen int
	if showBadge {
		label := "off"
		switch effort {
		case "low":
			label = "low"
		case "medium", "med":
			label = "med"
		case "high":
			label = "high"
		case "max":
			label = "max"
		}

		active := label != "off"
		iconStyle := borderStyle
		labelStyle := style.NewStyle().Foreground(theme.TextMuted)
		if active {
			if showThinking {
				iconStyle = style.NewStyle().Foreground(theme.Primary).Bold(true)
				labelStyle = style.NewStyle().Foreground(theme.Highlight).Bold(true)
			} else {
				iconStyle = style.NewStyle().Foreground(theme.Secondary)
				labelStyle = style.NewStyle().Foreground(theme.Secondary)
			}
		}

		statusRaw := " ∴ " + label
		statusLen = style.CellWidth(statusRaw)
		statusStyled = " " + iconStyle.Render("∴") + " " + labelStyle.Render(label)
	}

	if statusLen >= available {
		return borderStyle.Render(strings.Repeat("─", available))
	}

	var branchStyled string
	var branchLen int
	if len(gitBranch) > 0 && strings.TrimSpace(gitBranch[0]) != "" {
		b := strings.TrimSpace(SanitizeTerminalText(gitBranch[0]))
		if width < 70 && style.CellWidth(b) > 14 {
			b = style.TruncateCells(b, 14)
		}
		if b != "" {
			branchRaw := "─── ⎇ " + b + " "
			branchLen = style.CellWidth(branchRaw)
			if available > branchLen+statusLen+4 {
				branchStyled = borderStyle.Render("─── ") + branchStyle.Render("⎇ "+b) + " "
			} else {
				branchLen = 0
			}
		}
	}

	used := branchLen + statusLen
	if available <= used {
		branchStyled = ""
		branchLen = 0
		used = statusLen
	}

	dashCount := max(1, available-used)
	dashes := borderStyle.Render(strings.Repeat("─", dashCount))
	return branchStyled + dashes + statusStyled
}

// FormatPromptSeparator formats the horizontal line with optional reasoning status and git branch above the prompt.
func FormatPromptSeparator(showThinking bool, reasoningEffort string, theme style.UITheme, width int, gitBranch ...string) string {
	line := formatPromptSeparatorLine(showThinking, reasoningEffort, theme, width, gitBranch...)
	if line == "" {
		return "\n"
	}
	return line + "\n"
}

// FormatStaticStatsContent combines spinner and stats text with styling.
// The spinner frame is placed first so it stays anchored at column 1 and never
// shifts horizontally when the elapsed timer grows from seconds to minutes.
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
			return fmt.Sprintf("%s %s", displayFrame, statsText)
		}
		return displayFrame
	}
	return statsText
}
