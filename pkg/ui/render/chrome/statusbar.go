package chrome

import (
	"fmt"
	"math"
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
	HideWorkspace           bool
	HideSession             bool
	QueuedPromptsCount      int
	GitBranch               string
}

// formatCompactTokens formats a token count concisely without dead whitespace padding
// (for example "0", "950", "33.2k", "~33.2k", "128k", "1.2M").
func formatCompactTokens(tokens int, estimate bool) string {
	if tokens < 0 {
		tokens = 0
	}
	prefix := ""
	if estimate && tokens > 0 {
		prefix = "~"
	}
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%s%.1fM", prefix, float64(tokens)/1_000_000.0)
	case tokens >= 100_000:
		return fmt.Sprintf("%s%.0fk", prefix, float64(tokens)/1000.0)
	case tokens >= 1000:
		return fmt.Sprintf("%s%.1fk", prefix, float64(tokens)/1000.0)
	default:
		return fmt.Sprintf("%s%d", prefix, tokens)
	}
}

// formatCompactLimit formats a context window limit concisely (for example "128k", "1M").
func formatCompactLimit(limit int) string {
	if limit <= 0 {
		return "-"
	}
	switch {
	case limit >= 1_000_000:
		if limit%1_000_000 == 0 {
			return fmt.Sprintf("%dM", limit/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(limit)/1_000_000.0)
	case limit >= 1000:
		k := int(math.Round(float64(limit) / 1000.0))
		if k >= 1000 {
			return "1M"
		}
		return fmt.Sprintf("%dk", k)
	default:
		return fmt.Sprintf("%d", limit)
	}
}

// formatCompactPercent formats context fill percentage concisely (for example "0%", "0.4%", "27%", "~27%").
func formatCompactPercent(pct float64, estimate bool) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 999 {
		pct = 999
	}
	prefix := ""
	if estimate && pct > 0 {
		prefix = "~"
	}
	if pct > 0 && pct < 1 {
		return fmt.Sprintf("%s%.1f%%", prefix, pct)
	}
	return fmt.Sprintf("%s%.0f%%", prefix, math.Round(pct))
}

func formatCompactTps(tps float64) string {
	if tps >= 100 {
		return fmt.Sprintf("%.0f", min(9999.0, math.Round(tps)))
	}
	return fmt.Sprintf("%.1f", tps)
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
	filled := int(math.Round((pct / 100.0) * float64(barCells)))
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

// FormatStatusBarLeft renders compact token metrics, context usage, and speed within width.
func FormatStatusBarLeft(state StatusBarState, theme style.UITheme, width int) string {
	return formatStatusBarLeftForBudget(state, theme, width, width)
}

func formatStatusBarLeftForBudget(state StatusBarState, theme style.UITheme, _ int, maxCells int) string {
	if !state.ShowTokens {
		return ""
	}

	div := style.NewStyle().Foreground(theme.Border).Render(" │ ")
	labelStyle := style.NewStyle().Foreground(theme.TextMuted)
	valStyle := style.NewStyle().Foreground(theme.Text)
	speedStyle := style.NewStyle().Foreground(theme.TextMuted)

	inTok := formatCompactTokens(state.PromptTokens, state.TokenEstimate)
	outTok := formatCompactTokens(state.CompletionTokens, false)
	ioLine := " " + labelStyle.Render("↑") + valStyle.Render(inTok) + " " + labelStyle.Render("↓") + valStyle.Render(outTok)

	hasTps := (state.IsGenerating || state.HasLastTps) && state.LastTps > 0
	tpsSeg := ""
	if hasTps {
		tpsSeg = div + valStyle.Render(formatCompactTps(state.LastTps)) + speedStyle.Render("t/s")
	}

	var candidates []string
	if state.ContextLimit > 0 {
		// CompletionTokens is cumulative session usage and must not be treated
		// as tokens resident in the model's current context window.
		contextTokens := state.PromptTokens + state.CurrentCompletionTokens
		pct := (float64(contextTokens) / float64(state.ContextLimit)) * 100.0
		ctxEst := state.IsGenerating || state.TokenEstimate

		usedTok := formatCompactTokens(contextTokens, ctxEst)
		limitTok := formatCompactLimit(state.ContextLimit)
		pctTok := formatCompactPercent(pct, ctxEst)
		bar, ctxStyle := contextGaugeBar(pct, theme)

		ctxFull := bar + " " + ctxStyle.Render(pctTok+" "+usedTok+"/"+limitTok)
		ctxPctRatio := ctxStyle.Render(pctTok + " " + usedTok + "/" + limitTok)
		ctxRatio := ctxStyle.Render(usedTok + "/" + limitTok)
		ctxPct := ctxStyle.Render(pctTok)

		if hasTps {
			candidates = append(candidates,
				ioLine+div+ctxFull+tpsSeg,
				ioLine+div+ctxPctRatio+tpsSeg,
			)
		}
		candidates = append(candidates,
			ioLine+div+ctxFull,
			ioLine+div+ctxPctRatio,
		)
		if hasTps {
			candidates = append(candidates, ioLine+div+ctxPct+tpsSeg)
		}
		candidates = append(candidates,
			ioLine+div+ctxRatio,
			ioLine+div+ctxPct,
			ioLine+" "+ctxPct,
			ioLine,
			" "+ctxPct,
		)
	} else {
		if hasTps {
			candidates = append(candidates, ioLine+tpsSeg)
		}
		candidates = append(candidates, ioLine)
	}

	if maxCells <= 0 {
		return candidates[0]
	}
	for _, candidate := range candidates {
		if style.CellWidth(candidate) <= maxCells {
			return candidate
		}
	}
	return candidates[len(candidates)-1]
}

func buildStatusBarBadges(state StatusBarState, theme style.UITheme, compact bool) []string {
	var badges []string
	if state.QueuedPromptsCount > 0 {
		tag := fmt.Sprintf("[☰ queue:%d]", state.QueuedPromptsCount)
		if compact {
			tag = fmt.Sprintf("[☰ %d]", state.QueuedPromptsCount)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(tag))
	}

	if state.PlanTotal > 0 && state.PlanCompleted < state.PlanTotal {
		tag := fmt.Sprintf("[✓ plan:%d/%d]", state.PlanCompleted, state.PlanTotal)
		if compact {
			tag = fmt.Sprintf("[✓ %d/%d]", state.PlanCompleted, state.PlanTotal)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(tag))
	}

	if state.ActiveTasksCount > 0 {
		tag := fmt.Sprintf("[◫ tasks:%d]", state.ActiveTasksCount)
		if compact {
			tag = fmt.Sprintf("[◫ %d]", state.ActiveTasksCount)
		}
		badges = append(badges, style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(tag))
	}
	return badges
}

func fitStatusBarBadges(state StatusBarState, theme style.UITheme, width, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	if width >= 60 {
		if full := buildStatusBarBadges(state, theme, false); len(full) > 0 {
			part := strings.Join(full, " ") + " "
			if style.CellWidth(part) <= maxCells {
				return part
			}
		}
	}
	compact := buildStatusBarBadges(state, theme, true)
	if len(compact) == 0 {
		return ""
	}
	part := strings.Join(compact, " ") + " "
	if style.CellWidth(part) <= maxCells {
		return part
	}
	var fitted []string
	used := 0
	for _, b := range compact {
		add := style.CellWidth(b) + 1
		if used+add > maxCells {
			continue
		}
		fitted = append(fitted, b)
		used += add
	}
	if len(fitted) == 0 {
		return ""
	}
	return strings.Join(fitted, " ") + " "
}

func formatStatusBarRightWithBudget(state StatusBarState, theme style.UITheme, width, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)

	badgePart := fitStatusBarBadges(state, theme, width, maxCells)
	badgeWidth := style.CellWidth(badgePart)

	cleanModel := strings.TrimSpace(style.SanitizeTerminalText(state.Model))
	if width < 48 || cleanModel == "" {
		return badgePart
	}

	remForModel := maxCells - badgeWidth - 1 // trailing " "
	if remForModel < 4 {
		return badgePart
	}
	modelCap := min(remForModel, max(12, width/3))
	if width < 70 {
		modelCap = min(remForModel, 16)
	}
	truncatedModel := style.TruncateCells(cleanModel, modelCap)
	return badgePart + modelStyle.Render(truncatedModel) + " "
}

func formatWorkspaceSessionPrefix(state StatusBarState, theme style.UITheme, width int, hasTrailingMetrics bool) string {
	wsRaw := ""
	if !state.HideWorkspace {
		wsRaw = strings.TrimSpace(style.SanitizeTerminalText(state.Workspace))
	}
	sessRaw := ""
	if !state.HideSession {
		sessRaw = strings.TrimSpace(style.SanitizeTerminalText(state.SessionLabel))
	}
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

	if width < 70 {
		budget := max(6, min(14, width/4))
		if wsRaw != "" && sessRaw != "" {
			combined := wsRaw + "/" + sessRaw
			return " " + iconStyle.Render("⌂ ") + wsStyle.Render(style.TruncateCells(combined, budget)) + div
		}
		if wsRaw != "" {
			return " " + iconStyle.Render("⌂ ") + wsStyle.Render(style.TruncateCells(wsRaw, budget)) + div
		}
		return " " + iconStyle.Render("§ ") + sessStyle.Render(style.TruncateCells(sessRaw, budget)) + div
	}

	wsMax := 14
	sessMax := 10
	if width < 95 {
		wsMax = 12
		sessMax = 8
	}

	var parts []string
	if wsRaw != "" {
		parts = append(parts, iconStyle.Render("⌂ ")+wsStyle.Render(style.TruncateCells(wsRaw, wsMax)))
	}
	if sessRaw != "" {
		parts = append(parts, iconStyle.Render("§ ")+sessStyle.Render(style.TruncateCells(sessRaw, sessMax)))
	}
	return " " + strings.Join(parts, style.NewStyle().Foreground(theme.Border).Render(" │ ")) + div
}

// FormatStatusBarLine combines left and right status segments responsively across terminal widths.
func FormatStatusBarLine(state StatusBarState, theme style.UITheme, width int) string {
	if width <= 1 {
		return ""
	}
	available := width - 1
	prefixPart := formatWorkspaceSessionPrefix(state, theme, width, state.ShowTokens)
	prefixLen := style.CellWidth(prefixPart)

	badgeReserve := 0
	if width >= 60 {
		if full := buildStatusBarBadges(state, theme, false); len(full) > 0 {
			badgeReserve = style.CellWidth(strings.Join(full, " ")+" ") + 1
		}
	}
	if badgeReserve == 0 {
		if compact := buildStatusBarBadges(state, theme, true); len(compact) > 0 {
			badgeReserve = style.CellWidth(strings.Join(compact, " ")+" ") + 1
		}
	}

	modelReserve := 0
	cleanModel := strings.TrimSpace(style.SanitizeTerminalText(state.Model))
	if width >= 48 && cleanModel != "" {
		wantModel := min(style.CellWidth(cleanModel), max(10, min(20, width/4)))
		modelReserve = wantModel + 2 // gap + trailing space
	}

	reserveRight := badgeReserve + modelReserve
	metricsBudget := available - prefixLen - reserveRight
	if metricsBudget < 18 && badgeReserve > 0 {
		if compact := buildStatusBarBadges(state, theme, true); len(compact) > 0 {
			badgeReserve = style.CellWidth(strings.Join(compact, " ")+" ") + 1
			reserveRight = badgeReserve + modelReserve
			metricsBudget = available - prefixLen - reserveRight
		}
	}
	if metricsBudget < 18 && modelReserve > 0 {
		minModel := min(style.CellWidth(cleanModel), 8) + 2
		if available-prefixLen-badgeReserve-minModel >= 14 {
			modelReserve = minModel
		} else {
			modelReserve = 0
		}
		reserveRight = badgeReserve + modelReserve
		metricsBudget = available - prefixLen - reserveRight
	}

	metricsPart := ""
	if state.ShowTokens {
		metricsPart = formatStatusBarLeftForBudget(state, theme, width, max(1, metricsBudget))
	}
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
		collapseStyle = hoverButtonStyle(theme, theme.Primary)
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
			display = " ▸ output: folded "
		} else {
			display = " ▾ output: open "
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
