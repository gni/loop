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
	if effort != "off" && effort != "none" && effort != "" {
		thinkingText = effort
	}

	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
	prefix := "─── prompt "
	available := width - 1
	if available <= 0 {
		return "\n"
	}
	if available <= utf8.RuneCountInString(prefix) {
		return borderStyle.Render(style.TruncateRunes(prefix, available)) + "\n"
	}
	statusLen := utf8.RuneCountInString(statusPart)
	if available < utf8.RuneCountInString(prefix)+statusLen+3 {
		return borderStyle.Render(prefix+strings.Repeat("─", available-utf8.RuneCountInString(prefix))) + "\n"
	}
	dashes := strings.Repeat("─", available-utf8.RuneCountInString(prefix)-statusLen)
	return borderStyle.Render(prefix+dashes) + statusStyle.Render(statusPart) + "\n"
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
