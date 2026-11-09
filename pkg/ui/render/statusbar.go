package render

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

// FormatStatusBarLeft renders token metrics, TPS, and context usage on the left side of the status bar.
func FormatStatusBarLeft(state StatusBarState, theme style.UITheme, width int) string {
	if !state.ShowTokens {
		return ""
	}
	pStrCompact := fmt.Sprintf("%d↓", state.PromptTokens)
	if state.PromptTokens >= 1000 {
		pStrCompact = fmt.Sprintf("%.1fk↓", float64(state.PromptTokens)/1000.0)
	}

	cStrCompact := fmt.Sprintf("%d↑", state.CompletionTokens)
	if state.CompletionTokens >= 1000 {
		cStrCompact = fmt.Sprintf("%.1fk↑", float64(state.CompletionTokens)/1000.0)
	}

	pStr := fmt.Sprintf("%d in", state.PromptTokens)
	if state.PromptTokens >= 1000 {
		pStr = fmt.Sprintf("%.1fk in", float64(state.PromptTokens)/1000.0)
	}
	if state.TokenEstimate {
		pStr = "~" + pStr
		pStrCompact = "~" + pStrCompact
	}

	cStr := fmt.Sprintf("%d out", state.CompletionTokens)
	if state.CompletionTokens >= 1000 {
		cStr = fmt.Sprintf("%.1fk out", float64(state.CompletionTokens)/1000.0)
	}
	if (state.IsGenerating || state.HasLastTps) && state.LastTps > 0 {
		cStr += fmt.Sprintf(" (%.1f t/s)", state.LastTps)
	}

	// CompletionTokens is cumulative session usage and must not be treated
	// as tokens resident in the model's current context window.
	contextTokens := state.PromptTokens + state.CurrentCompletionTokens
	var pct float64
	if state.ContextLimit > 0 {
		pct = (float64(contextTokens) / float64(state.ContextLimit)) * 100.0
	}

	totStr := fmt.Sprintf("%d", contextTokens)
	if contextTokens >= 1000 {
		totStr = fmt.Sprintf("%.1fk", float64(contextTokens)/1000.0)
	}
	pctStr := fmt.Sprintf("%.1f%%", pct)
	if state.IsGenerating || state.TokenEstimate {
		totStr = "~" + totStr
		pctStr = "~" + pctStr
	}

	limitStr := fmt.Sprintf("%d", state.ContextLimit)
	if state.ContextLimit >= 1000 {
		limitStr = fmt.Sprintf("%dk", state.ContextLimit/1000)
	}

	var ctxStr string
	if width < 70 {
		ctxStr = fmt.Sprintf("%s/%s", totStr, limitStr)
	} else {
		ctxStr = fmt.Sprintf("%s/%s (%s)", totStr, limitStr, pctStr)
	}

	pStyle := style.NewStyle().Foreground(theme.Secondary)
	cStyle := style.NewStyle().Foreground(theme.Highlight)
	ctxStyle := style.NewStyle().Foreground(theme.Primary)

	if width < 40 {
		return fmt.Sprintf(" %s %s", pStyle.Render(pStrCompact), cStyle.Render(cStrCompact))
	}
	if width < 55 {
		return fmt.Sprintf(" %s  %s", pStyle.Render(pStr), cStyle.Render(cStr))
	}
	if width >= 75 {
		pStr = fmt.Sprintf("%-9s", pStr)
		cStr = fmt.Sprintf("%-21s", cStr)
		ctxStr = fmt.Sprintf("%-28s", ctxStr)
	}
	return fmt.Sprintf(" %s   %s   %s", pStyle.Render(pStr), cStyle.Render(cStr), ctxStyle.Render(ctxStr))
}

// FormatStatusBarRight renders queue count, plan status, tasks, and model name on the right side.
func FormatStatusBarRight(state StatusBarState, theme style.UITheme, width int) string {
	if state.Model == "" {
		return ""
	}
	modelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)

	queueStr := ""
	if state.QueuedPromptsCount > 0 {
		tag := fmt.Sprintf("[queue:%d]", state.QueuedPromptsCount)
		if width < 50 {
			tag = fmt.Sprintf("q:%d", state.QueuedPromptsCount)
		}
		queueStr = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(tag) + " "
	}

	taskStr := ""
	if state.ActiveTasksCount > 0 {
		tag := fmt.Sprintf("[tasks:%d]", state.ActiveTasksCount)
		if width < 40 {
			tag = fmt.Sprintf("t:%d", state.ActiveTasksCount)
		} else if width < 60 {
			tag = fmt.Sprintf("[t:%d]", state.ActiveTasksCount)
		}
		taskStr = style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(tag) + " "
	}

	planStr := ""
	if state.PlanTotal > 0 && state.PlanCompleted < state.PlanTotal {
		planStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		if width < 50 {
			planStr = planStyle.Render(fmt.Sprintf("[%d/%d]", state.PlanCompleted, state.PlanTotal)) + " "
		} else {
			planStr = planStyle.Render(fmt.Sprintf("[plan:%d/%d]", state.PlanCompleted, state.PlanTotal)) + " "
		}
	}

	rightInfo := queueStr + planStr + taskStr
	if width < 45 {
		return rightInfo
	} else if width < 65 {
		if state.Model == "" {
			return rightInfo
		}
		modelName := style.TruncateCells(state.Model, 13)
		return rightInfo + modelStyle.Render(modelName) + " "
	} else {
		if state.Model == "" {
			return rightInfo
		}
		return rightInfo + modelStyle.Render(style.TruncateCells(state.Model, max(8, width/3))) + " "
	}
}

// FormatStatusBarLine combines left and right status segments with appropriate terminal padding.
func FormatStatusBarLine(state StatusBarState, theme style.UITheme, width int) string {
	if width <= 1 {
		return ""
	}
	available := width - 1
	leftPart := FormatStatusBarLeft(state, theme, width)
	if state.Workspace != "" || state.SessionLabel != "" {
		budget := max(5, min(36, width/2))
		sessionBudget := max(2, (budget-3)*2/3)
		workspaceBudget := max(2, budget-3-sessionBudget)
		workspace := style.TruncateCells(SanitizeTerminalText(state.Workspace), workspaceBudget)
		session := style.TruncateCells(SanitizeTerminalText(state.SessionLabel), sessionBudget)
		contextLabel := strings.Trim(workspace+" / "+session, " / ")
		leftPart = " " + style.NewStyle().Foreground(theme.TextMuted).Render(contextLabel) + leftPart
	}
	rightPart := FormatStatusBarRight(state, theme, width)
	leftLen := style.CellWidth(leftPart)
	rightLen := style.CellWidth(rightPart)

	if leftLen+rightLen+1 > available {
		rightPart = ""
		rightLen = 0
	}
	if leftLen > available {
		return style.TruncateCells(style.StripAnsi(leftPart), available)
	}
	padding := available - leftLen - rightLen
	return fmt.Sprintf("%s%s%s", leftPart, strings.Repeat(" ", padding), rightPart)
}

// FormatStatusBarSeparator formats the horizontal separator line and collapse indicator above the status bar.
func FormatStatusBarSeparator(width int, indicator string, theme style.UITheme) string {
	borderStyle := style.NewStyle().Foreground(theme.Border)
	collapseStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)

	indicatorLen := utf8.RuneCountInString(indicator)
	dashesCount := (width - 1) - indicatorLen - 1 // space + indicator
	if dashesCount < 1 {
		dashesCount = 1
	}
	return borderStyle.Render(strings.Repeat("─", dashesCount)) + " " + collapseStyle.Render(indicator)
}
