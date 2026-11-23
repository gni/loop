package chrome

import (
	"fmt"
	"strings"
	"time"

	"loop/pkg/ui/style"
)

// StatusBarState represents the snapshot of metrics displayed in the status bar.
type StatusBarState struct {
	Workspace, SessionLabel string
	Model                   string
	PromptTokens            int
	CompletionTokens        int
	CurrentCompletionTokens int // Used to calculate current t/s speed
	ContextLimit            int
	StartTime               time.Time
	IsGenerating            bool
	TokenEstimate           bool
	LastTps                 float64
	HasLastTps              bool
	ActiveTasksCount        int
	PlanCompleted           int
	PlanTotal               int
	ShowTokens              bool
	QueuedPromptsCount      int
	GitBranch               string
}

// formatTabularTokens formats a token count into a fixed 7-column right-aligned slot
// (for example "      0", "   ~950", " ~63.6k", "~128.4k", "  1.25M") so changing values
// or estimate prefixes (~) never shift adjacent elements in the status bar.
func formatTabularTokens(tokens int, estimate bool) string {
	if tokens < 0 {
		tokens = 0
	}
	prefix := ""
	if estimate && tokens > 0 {
		prefix = "~"
	}
	var raw string
	switch {
	case tokens >= 1_000_000:
		raw = fmt.Sprintf("%s%.2fM", prefix, float64(tokens)/1_000_000.0)
	case tokens >= 100_000:
		raw = fmt.Sprintf("%s%.1fk", prefix, float64(tokens)/1000.0)
	case tokens >= 1000:
		raw = fmt.Sprintf("%s%.1fk", prefix, float64(tokens)/1000.0)
	default:
		raw = fmt.Sprintf("%s%d", prefix, tokens)
	}
	return padLeftCells(raw, 7)
}

// formatTabularLimit formats a context window limit into a fixed 5-column left-aligned slot.
func formatTabularLimit(limit int) string {
	if limit <= 0 {
		return style.PadRightCells("  -  ", 5)
	}
	var raw string
	switch {
	case limit >= 1_000_000:
		if limit%1_000_000 == 0 {
			raw = fmt.Sprintf("%dM", limit/1_000_000)
		} else {
			raw = fmt.Sprintf("%.1fM", float64(limit)/1_000_000.0)
		}
	case limit >= 1000:
		raw = fmt.Sprintf("%dk", limit/1000)
	default:
		raw = fmt.Sprintf("%d", limit)
	}
	return style.PadRightCells(raw, 5)
}

// formatTabularPercent formats a context percentage into a fixed 6-column "~56.2%" slot.
func formatTabularPercent(pct float64, estimate bool) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 999.9 {
		pct = 999.9
	}
	prefix := ""
	if estimate && pct > 0 {
		prefix = "~"
	}
	inner := fmt.Sprintf("%s%.1f%%", prefix, pct)
	return padLeftCells(inner, 6)
}

func padLeftCells(s string, width int) string {
	w := style.CellWidth(s)
	if w >= width {
		return style.TruncateCells(s, width)
	}
	return strings.Repeat(" ", width-w) + s
}

// contextGaugeBar returns a fixed 5-cell progress bar and threshold style.
func contextGaugeBar(pct float64, theme style.UITheme) (string, style.Style) {
	barTint := theme.Primary
	metricTint := theme.Text
	switch {
	case pct >= 90:
		barTint = theme.Error
		metricTint = theme.Error
	case pct >= 75:
		barTint = theme.Warning
		metricTint = theme.Warning
	}

	const barCells = 5
	filled := int((pct / 100.0) * float64(barCells))
	if pct > 0 && filled == 0 {
		filled = 1
	}
	if filled > barCells {
		filled = barCells
	}
	if filled < 0 {
		filled = 0
	}
	filledPart := style.NewStyle().Foreground(barTint).Render(strings.Repeat("▰", filled))
	emptyPart := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("▱", barCells-filled))
	return filledPart + emptyPart, style.NewStyle().Foreground(metricTint)
}

// FormatStatusBarLeft renders token metrics, TPS, and context usage in fixed-width tabular columns.
func FormatStatusBarLeft(state StatusBarState, theme style.UITheme, width int) string {
	if !state.ShowTokens {
		return ""
	}

	div := style.NewStyle().Foreground(theme.Border).Render(" │ ")
	labelStyle := style.NewStyle().Foreground(theme.TextMuted)
	valStyle := style.NewStyle().Foreground(theme.Text)
	speedStyle := style.NewStyle().Foreground(theme.TextMuted)

	inVal := formatTabularTokens(state.PromptTokens, state.TokenEstimate)
	outVal := formatTabularTokens(state.CompletionTokens, false)

	if width < 44 {
		pCompact := labelStyle.Render("↑") + valStyle.Render(strings.TrimSpace(inVal))
		cCompact := labelStyle.Render("↓") + valStyle.Render(strings.TrimSpace(outVal))
		return fmt.Sprintf(" %s %s", pCompact, cCompact)
	}

	inSeg := labelStyle.Render("↑ in ") + valStyle.Render(inVal)
	outSeg := labelStyle.Render("↓ out ") + valStyle.Render(outVal)

	if width < 64 {
		return " " + inSeg + div + outSeg
	}

	// CompletionTokens is cumulative session usage and must not be treated
	// as tokens resident in the model's current context window.
	contextTokens := state.PromptTokens + state.CurrentCompletionTokens
	var pct float64
	if state.ContextLimit > 0 {
		pct = (float64(contextTokens) / float64(state.ContextLimit)) * 100.0
	}

	ctxEst := state.IsGenerating || state.TokenEstimate
	usedVal := formatTabularTokens(contextTokens, ctxEst)
	limitVal := formatTabularLimit(state.ContextLimit)
	pctVal := formatTabularPercent(pct, ctxEst)
	bar, ctxStyle := contextGaugeBar(pct, theme)

	var ctxSeg string
	if width < 92 {
		ctxSeg = labelStyle.Render("ctx ") + ctxStyle.Render(fmt.Sprintf("%s/%s", usedVal, strings.TrimRight(limitVal, " ")))
	} else if width < 106 {
		ctxSeg = labelStyle.Render("ctx ") + ctxStyle.Render(fmt.Sprintf("%s %s/%s", pctVal, usedVal, limitVal))
	} else {
		ctxSeg = labelStyle.Render("ctx ") + bar + ctxStyle.Render(fmt.Sprintf(" %s %s/%s", pctVal, usedVal, limitVal))
	}

	line := " " + inSeg + div + outSeg + div + ctxSeg

	if (state.IsGenerating || state.HasLastTps) && state.LastTps > 0 && width >= 116 {
		tpsVal := fmt.Sprintf("%5.1f", min(999.9, state.LastTps))
		line += div + speedStyle.Render("» ") + valStyle.Render(tpsVal) + speedStyle.Render(" t/s")
	}

	return line
}

// FormatStatusBarRight renders queue count, plan status, tasks, and model name on the right side.
func FormatStatusBarRight(state StatusBarState, theme style.UITheme, width int) string {
	return formatStatusBarRightWithBudget(state, theme, width, max(0, width-1))
}

func formatStatusBarRightWithBudget(state StatusBarState, theme style.UITheme, width, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)

	var badges []string
	if state.QueuedPromptsCount > 0 {
		tag := fmt.Sprintf("[☰ queue:%d]", state.QueuedPromptsCount)
		if width < 60 {
			tag = fmt.Sprintf("[☰ %d]", state.QueuedPromptsCount)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(tag))
	}

	if state.PlanTotal > 0 && state.PlanCompleted < state.PlanTotal {
		tag := fmt.Sprintf("[✓ plan:%d/%d]", state.PlanCompleted, state.PlanTotal)
		if width < 60 {
			tag = fmt.Sprintf("[✓ %d/%d]", state.PlanCompleted, state.PlanTotal)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(tag))
	}

	if state.ActiveTasksCount > 0 {
		tag := fmt.Sprintf("[◫ tasks:%d]", state.ActiveTasksCount)
		if width < 60 {
			tag = fmt.Sprintf("[◫ %d]", state.ActiveTasksCount)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(tag))
	}

	badgePart := ""
	if len(badges) > 0 {
		badgePart = strings.Join(badges, " ") + " "
	}
	badgeWidth := style.CellWidth(badgePart)
	if badgeWidth > maxCells {
		return ""
	}

	cleanModel := strings.TrimSpace(style.SanitizeTerminalText(state.Model))
	if width < 48 || cleanModel == "" {
		return badgePart
	}

	remForModel := maxCells - badgeWidth - 1 // trailing " "
	if remForModel < 2 {
		return badgePart
	}
	modelCap := min(remForModel, max(12, width/4))
	if width < 70 {
		modelCap = min(remForModel, 14)
	}
	truncatedModel := style.TruncateCells(cleanModel, modelCap)
	return badgePart + modelStyle.Render(truncatedModel) + " "
}

func formatWorkspaceSessionPrefix(state StatusBarState, theme style.UITheme, width int, hasTrailingMetrics bool) string {
	wsRaw := strings.TrimSpace(style.SanitizeTerminalText(state.Workspace))
	sessRaw := strings.TrimSpace(style.SanitizeTerminalText(state.SessionLabel))
	if wsRaw == "" && sessRaw == "" {
		return ""
	}

	div := ""
	if hasTrailingMetrics {
		div = style.NewStyle().Foreground(theme.Border).Render(" │")
	}
	wsStyle := style.NewStyle().Foreground(theme.TextMuted)
	sessStyle := style.NewStyle().Foreground(theme.TextMuted)
	iconStyle := style.NewStyle().Foreground(theme.Border)

	// Keep workspace and session inside deterministic column slots so changing
	// session IDs or titles never shifts the token columns to the right.
	if width < 65 {
		budget := max(6, min(18, width/3))
		combined := strings.Trim(wsRaw+"/"+sessRaw, "/")
		return " " + iconStyle.Render("⌂ ") + wsStyle.Render(style.PadRightCells(style.TruncateCells(combined, budget), budget)) + div
	}

	wsSlot := min(14, max(4, style.CellWidth(wsRaw)))
	const sessSlot = 10

	var parts []string
	if wsRaw != "" {
		wsFixed := style.PadRightCells(style.TruncateCells(wsRaw, wsSlot), wsSlot)
		parts = append(parts, iconStyle.Render("⌂ ")+wsStyle.Render(wsFixed))
	}
	if sessRaw != "" {
		sessFixed := style.PadRightCells(style.TruncateCells(sessRaw, sessSlot), sessSlot)
		parts = append(parts, iconStyle.Render("§ ")+sessStyle.Render(sessFixed))
	}
	return " " + strings.Join(parts, style.NewStyle().Foreground(theme.Border).Render(" │ ")) + div
}

// FormatStatusBarLine combines left and right status segments with deterministic tabular alignment.
func FormatStatusBarLine(state StatusBarState, theme style.UITheme, width int) string {
	if width <= 1 {
		return ""
	}
	available := width - 1
	metricsPart := FormatStatusBarLeft(state, theme, width)
	prefixPart := formatWorkspaceSessionPrefix(state, theme, width, metricsPart != "")
	leftPart := prefixPart + metricsPart

	leftLen := style.CellWidth(leftPart)
	if leftLen > available {
		return style.TruncateCells(leftPart, available)
	}

	remRight := available - leftLen - 1
	rightPart := formatStatusBarRightWithBudget(state, theme, width, remRight)
	rightLen := style.CellWidth(rightPart)
	if leftLen+rightLen > available {
		rightPart = ""
		rightLen = 0
	}

	padding := max(0, available-leftLen-rightLen)
	return leftPart + strings.Repeat(" ", padding) + rightPart
}

// FormatStatusBarSeparator formats the horizontal separator line and collapse indicator above the status bar.
func FormatStatusBarSeparator(width int, indicator string, theme style.UITheme) string {
	line, _ := FormatStatusBarSeparatorLayout(width, 0, indicator, false, false, theme)
	return line
}

// FormatStatusBarSeparatorLayout formats the status separator with optional hover expansion
// and returns the 1-based hitbox for the collapse toggle indicator.
func FormatStatusBarSeparatorLayout(width, row int, indicator string, collapsed, hovered bool, theme style.UITheme) (string, *Hitbox) {
	available := width - 1
	if available <= 0 {
		return "", nil
	}
	borderStyle := style.NewStyle().Foreground(theme.Border)
	collapseStyle := style.NewStyle().Foreground(theme.TextMuted)
	if hovered {
		collapseStyle = style.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	if indicator == "" {
		return borderStyle.Render(strings.Repeat("─", available)), nil
	}
	if indicator == "›" {
		indicator = "▸"
	}
	display := indicator
	padHit := 2
	if hovered {
		if collapsed || indicator == "▸" {
			display = "▸ output: folded"
		} else {
			display = "▾ output: open"
		}
		padHit = 0
	}
	indicatorLen := style.CellWidth(display)
	if indicatorLen >= available {
		return collapseStyle.Render(style.TruncateCells(display, available)), nil
	}
	dashesCount := available - indicatorLen - 1
	var hb *Hitbox
	if row > 0 {
		startCol := max(1, available-indicatorLen+1-padHit)
		hb = &Hitbox{
			Target:   HoverCollapseBadge,
			Row:      row,
			StartCol: startCol,
			EndCol:   available,
		}
	}
	if dashesCount <= 0 {
		return collapseStyle.Render(display), hb
	}
	return borderStyle.Render(strings.Repeat("─", dashesCount)) + " " + collapseStyle.Render(display), hb
}
