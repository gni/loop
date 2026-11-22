package render

import (
	"fmt"
	"strings"

	"loop/pkg/ui/style"
)

func formatPromptSeparatorLine(showThinking bool, reasoningEffort string, showAutoBadge, autoApprove bool, theme style.UITheme, width int, gitBranch ...string) string {
	line, _ := formatPromptSeparatorLayout(showThinking, reasoningEffort, showAutoBadge, autoApprove, false, false, HoverNone, 0, theme, width, gitBranch...)
	return line
}

func formatPromptSeparatorLayout(showThinking bool, reasoningEffort string, showAutoBadge, autoApprove, showCommandsBtn, commandsOpen bool, hovered HoverTarget, sepRow int, theme style.UITheme, width int, gitBranch ...string) (string, []Hitbox) {
	available := width - 1
	if available <= 0 {
		return "", nil
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	branchStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
	autoOnStyle := style.NewStyle().Foreground(theme.Warning).Bold(true)
	autoOffStyle := style.NewStyle().Foreground(theme.Border)
	autoOffHoverStyle := style.NewStyle().Foreground(theme.TextMuted)

	// Optional right-hand badges: slash commands button ("/"), loop icon ("♾︎",
	// bold amber when auto_approve is on, dim grey when off so it remains
	// clickable/tappable on desktop and mobile), and reasoning badge
	// ("∴ low", "∴ med", "∴ high", "∴ max", "∴ off") unless hidden.
	// When hovered, each badge expands inline like a tooltip.
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	showBadge := effort != "hidden"

	type rightPart struct {
		target HoverTarget
		raw    string
		styled string
		padHit int
	}
	var rightParts []rightPart
	if showCommandsBtn {
		raw := "/"
		padHit := 1
		st := style.NewStyle().Foreground(theme.TextMuted)
		if commandsOpen {
			st = style.NewStyle().Foreground(theme.Primary).Bold(true)
		}
		if hovered == HoverCommandsButton {
			raw = "/ commands"
			padHit = 0
			if commandsOpen {
				st = style.NewStyle().Foreground(theme.Primary).Bold(true)
			} else {
				st = style.NewStyle().Foreground(theme.Highlight).Bold(true)
			}
		}
		rightParts = append(rightParts, rightPart{
			target: HoverCommandsButton,
			raw:    raw,
			styled: st.Render(raw),
			padHit: padHit,
		})
	}
	if showAutoBadge {
		raw := "♾\uFE0E"
		padHit := 1
		var st style.Style
		if autoApprove {
			st = autoOnStyle
			if hovered == HoverAutoBadge {
				raw = "♾\uFE0E auto mode on"
				padHit = 0
			}
		} else {
			st = autoOffStyle
			if hovered == HoverAutoBadge {
				raw = "♾\uFE0E auto mode off"
				st = autoOffHoverStyle
				padHit = 0
			}
		}
		rightParts = append(rightParts, rightPart{
			target: HoverAutoBadge,
			raw:    raw,
			styled: st.Render(raw),
			padHit: padHit,
		})
	}
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
		if hovered == HoverReasoningBadge {
			iconStyle = style.NewStyle().Foreground(theme.Primary).Bold(true)
			labelStyle = style.NewStyle().Foreground(theme.Highlight).Bold(true)
			label = "reasoning: " + label
		}

		rightParts = append(rightParts, rightPart{
			target: HoverReasoningBadge,
			raw:    "∴ " + label,
			styled: iconStyle.Render("∴") + " " + labelStyle.Render(label),
		})
	}

	var statusStyled string
	var statusLen int
	if len(rightParts) > 0 {
		rawParts := make([]string, len(rightParts))
		styledParts := make([]string, len(rightParts))
		for i, part := range rightParts {
			rawParts[i] = part.raw
			styledParts[i] = part.styled
		}
		statusRaw := " " + strings.Join(rawParts, " · ")
		statusLen = style.CellWidth(statusRaw)
		statusStyled = " " + strings.Join(styledParts, borderStyle.Render(" · "))
	}

	if statusLen >= available {
		return borderStyle.Render(strings.Repeat("─", available)), nil
	}

	var branchStyled string
	var branchLen int
	var branchHit *Hitbox
	if len(gitBranch) > 0 && strings.TrimSpace(gitBranch[0]) != "" {
		b := strings.TrimSpace(SanitizeTerminalText(gitBranch[0]))
		if width < 70 && style.CellWidth(b) > 14 {
			b = style.TruncateCells(b, 14)
		}
		if b != "" {
			branchDisplay := "⎇ " + b
			if hovered == HoverGitBranch {
				branchDisplay = "⎇ branch: " + b
				branchStyle = style.NewStyle().Foreground(theme.Highlight).Bold(true)
			}
			branchRaw := "─── " + branchDisplay + " "
			branchLen = style.CellWidth(branchRaw)
			if available > branchLen+statusLen+4 {
				branchStyled = borderStyle.Render("─── ") + branchStyle.Render(branchDisplay) + " "
				if sepRow > 0 {
					startCol := 5 // 1-based after "─── "
					endCol := startCol + style.CellWidth(branchDisplay) - 1
					branchHit = &Hitbox{Target: HoverGitBranch, Row: sepRow, StartCol: startCol, EndCol: endCol}
				}
			} else {
				branchLen = 0
			}
		}
	}

	used := branchLen + statusLen
	if available <= used {
		branchStyled = ""
		branchLen = 0
		branchHit = nil
		used = statusLen
	}

	dashCount := max(1, available-used)
	dashes := borderStyle.Render(strings.Repeat("─", dashCount))

	var hitboxes []Hitbox
	if sepRow > 0 {
		if branchHit != nil {
			hitboxes = append(hitboxes, *branchHit)
		}
		// Compute exact 1-based column spans for each right-hand badge.
		// Left section takes branchLen + dashCount cells, followed by leading space (" ").
		col := branchLen + dashCount + 2
		for i, part := range rightParts {
			if i > 0 {
				col += 3 // " · "
			}
			w := max(1, style.CellWidth(part.raw))
			startCol := max(1, col-part.padHit)
			endCol := min(available, col+w-1+part.padHit)
			hitboxes = append(hitboxes, Hitbox{
				Target:   part.target,
				Row:      sepRow,
				StartCol: startCol,
				EndCol:   endCol,
			})
			col += w
		}
	}

	return branchStyled + dashes + statusStyled, hitboxes
}

// FormatPromptSeparator formats the horizontal line with optional reasoning status and git branch above the prompt.
func FormatPromptSeparator(showThinking bool, reasoningEffort string, theme style.UITheme, width int, gitBranch ...string) string {
	return FormatPromptSeparatorWithAuto(showThinking, reasoningEffort, false, false, theme, width, gitBranch...)
}

// FormatPromptSeparatorWithAuto formats the horizontal prompt separator with optional auto-approve, reasoning, and git branch indicators.
func FormatPromptSeparatorWithAuto(showThinking bool, reasoningEffort string, showAutoBadge, autoApprove bool, theme style.UITheme, width int, gitBranch ...string) string {
	line := formatPromptSeparatorLine(showThinking, reasoningEffort, showAutoBadge, autoApprove, theme, width, gitBranch...)
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
