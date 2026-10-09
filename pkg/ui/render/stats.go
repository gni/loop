package render

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

// FormatPromptSeparator formats the horizontal line with reasoning status above the prompt.
func FormatPromptSeparator(showThinking bool, reasoningEffort string, theme style.UITheme, width int) string {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	thinkingText := "off"
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	if showThinking && effort != "off" && effort != "none" {
		if effort == "" {
			thinkingText = "low"
		} else {
			thinkingText = effort
		}
	}

	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
	prefixCombined := borderStyle.Render("─── prompt ")

	statusLen := utf8.RuneCountInString(style.StripAnsi(statusPart))
	prefixLen := 11

	dashesCount := width - prefixLen - statusLen - 2
	if dashesCount < 3 {
		dashesCount = 3
	}
	dashes := strings.Repeat("─", dashesCount)
	return fmt.Sprintf("%s%s\n", prefixCombined+borderStyle.Render(dashes), statusStyle.Render(statusPart))
}

// FormatStaticStatsContent combines spinner and stats text with styling.
func FormatStaticStatsContent(spinnerFrame string, statsText string, theme style.UITheme) string {
	if spinnerFrame != "" {
		displayFrame := spinnerFrame
		if !strings.Contains(spinnerFrame, "\x1b[") {
			displayFrame = style.NewStyle().Foreground(theme.Primary).Bold(true).Render(spinnerFrame)
		}
		if statsText != "" {
			return fmt.Sprintf("%s %s", displayFrame, statsText)
		}
		return displayFrame
	}
	return statsText
}
