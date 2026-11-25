package chrome

import (
	"fmt"
	"image/color"
	"strings"

	"loop/pkg/ui/style"
)

func hoverButtonStyle(theme style.UITheme, accent color.Color) style.Style {
	if accent == nil {
		accent = theme.Primary
	}
	if theme.Background != nil {
		return style.NewStyle().Foreground(theme.Background).Background(accent).Bold(true)
	}
	return style.NewStyle().Foreground(accent).Bold(true).Underline(true)
}

func formatPromptSeparatorLine(showThinking bool, reasoningEffort string, showAutoBadge, autoApprove bool, theme style.UITheme, width int, gitBranch ...string) string {
	line, _ := formatPromptSeparatorLayout(showThinking, reasoningEffort, showAutoBadge, autoApprove, false, false, "", HoverNone, 0, theme, width, gitBranch...)
	return line
}

func formatPromptSeparatorLayout(showThinking bool, reasoningEffort string, showAutoBadge, autoApprove, showCommandsBtn, commandsOpen bool, activeThemeName string, hovered HoverTarget, sepRow int, theme style.UITheme, width int, gitBranch ...string) (string, []Hitbox) {
	available := width - 1
	if available <= 0 {
		return "", nil
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	branchStyle := style.NewStyle().Foreground(theme.TextMuted)
	autoOnStyle := style.NewStyle().Foreground(theme.Secondary)
	autoOffStyle := style.NewStyle().Foreground(theme.Border)

	// Optional right-hand badges: slash commands button ("/"), theme switcher ("◐"),
	// loop icon ("∞", soft theme secondary accent when auto_approve is on, dim border
	// color when off so it remains clickable/tappable on desktop and mobile), and
	// reasoning badge ("∴ low", "∴ med", "∴ high", "∴ max", "∴ off") unless hidden.
	// When hovered, each badge expands inline like a tooltip.
	effort := strings.ToLower(strings.TrimSpace(reasoningEffort))
	showBadge := effort != "hidden"

	type rightPart struct {
		target   HoverTarget
		raw      string
		styled   string
		padLeft  int
		padRight int
	}
	var rightParts []rightPart
	if showCommandsBtn {
		raw := "/"
		padL, padR := 1, 1
		st := style.NewStyle().Foreground(theme.TextMuted)
		if commandsOpen {
			st = style.NewStyle().Foreground(theme.Primary).Bold(true)
		}
		if hovered == HoverCommandsButton {
			raw = " / commands "
			padL, padR = 0, 1
			st = hoverButtonStyle(theme, theme.Primary)
		}
		rightParts = append(rightParts, rightPart{
			target:   HoverCommandsButton,
			raw:      raw,
			styled:   st.Render(raw),
			padLeft:  padL,
			padRight: padR,
		})

		themeLabel := style.TruncateCells(strings.TrimSpace(activeThemeName), 20)
		if themeLabel == "" {
			themeLabel = "studio"
		}
		themeRaw := "◐"
		themePadL, themePadR := 1, 1
		themeSt := style.NewStyle().Foreground(theme.Primary)
		if hovered == HoverThemeButton {
			themeRaw = " ◐ theme: " + themeLabel + " "
			themePadL, themePadR = 0, 1
			themeSt = hoverButtonStyle(theme, theme.Primary)
		}
		rightParts = append(rightParts, rightPart{
			target:   HoverThemeButton,
			raw:      themeRaw,
			styled:   themeSt.Render(themeRaw),
			padLeft:  themePadL,
			padRight: themePadR,
		})
	}
	if showAutoBadge {
		raw := "∞"
		padL, padR := 1, 1
		var st style.Style
		if autoApprove {
			st = autoOnStyle
			if hovered == HoverAutoBadge {
				raw = " ∞ auto: on "
				st = hoverButtonStyle(theme, theme.Secondary)
				padL, padR = 0, 1
			}
		} else {
			st = autoOffStyle
			if hovered == HoverAutoBadge {
				raw = " ∞ auto: off "
				st = hoverButtonStyle(theme, theme.TextMuted)
				padL, padR = 0, 1
			}
		}
		rightParts = append(rightParts, rightPart{
			target:   HoverAutoBadge,
			raw:      raw,
			styled:   st.Render(raw),
			padLeft:  padL,
			padRight: padR,
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
				iconStyle = style.NewStyle().Foreground(theme.Primary)
				labelStyle = style.NewStyle().Foreground(theme.Primary)
			} else {
				iconStyle = style.NewStyle().Foreground(theme.TextMuted)
				labelStyle = style.NewStyle().Foreground(theme.TextMuted)
			}
		}
		raw := "∴ " + label
		styled := iconStyle.Render("∴") + " " + labelStyle.Render(label)
		if hovered == HoverReasoningBadge {
			raw = " ∴ reasoning: " + label + " "
			styled = hoverButtonStyle(theme, theme.Primary).Render(raw)
		}

		rightParts = append(rightParts, rightPart{
			target: HoverReasoningBadge,
			raw:    raw,
			styled: styled,
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
		b := strings.TrimSpace(style.SanitizeTerminalText(gitBranch[0]))
		if width < 70 && style.CellWidth(b) > 14 {
			b = style.TruncateCells(b, 14)
		}
		if b != "" {
			branchDisplay := "⎇ " + b
			if hovered == HoverGitBranch {
				branchDisplay = "⎇ branch: " + b
				branchStyle = style.NewStyle().Foreground(theme.Primary).Bold(true)
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
			startCol := max(1, col-part.padLeft)
			endCol := min(available, col+w-1+part.padRight)
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
