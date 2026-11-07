package render

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"strings"
	"unicode"

	"loop/pkg/domain/task"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// Presentation only: the tool's JSON stays unchanged in model and saved history.
func renderTaskState(w io.Writer, output string, collapse bool, theme style.UITheme) bool {
	var state task.State
	if json.Unmarshal([]byte(output), &state) != nil || state.Status == "" {
		return false
	}
	label, symbol, tint := taskStateAppearance(state.Status, theme)
	fmt.Fprintln(w, style.NewStyle().Foreground(tint).Bold(true).Render("⎿  "+symbol+" "+label))
	width, _ := terminal.GetDimensions()
	text := func(content, prefix string, tint color.Color) {
		content = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) && r != '\n' {
				return -1
			}
			return r
		}, style.StripAnsi(content))
		for i, row := range ChatHistoryRows(strings.TrimSpace(content), max(1, width-7)) {
			lead := "   "
			if i == 0 {
				lead += prefix
			} else {
				lead += strings.Repeat(" ", len([]rune(prefix)))
			}
			fmt.Fprintln(w, lead+style.NewStyle().Foreground(tint).Render(row))
		}
	}
	summary := state.Summary
	if strings.TrimSpace(summary) == "" {
		summary = state.ExpectedOutcome
	}
	if strings.TrimSpace(summary) == "" {
		summary = state.Objective
	}
	if strings.TrimSpace(summary) != "" {
		text(summary, "", theme.Text)
	}
	if len(state.Plan) > 0 {
		completed := 0
		for _, item := range state.Plan {
			if item.Status == task.Completed {
				completed++
			}
		}
		text(fmt.Sprintf("%d/%d steps completed", completed, len(state.Plan)), "", theme.TextMuted)
		if !collapse {
			for _, item := range state.Plan {
				_, mark, tint := taskStateAppearance(item.Status, theme)
				text(item.Description, mark+" ", tint)
			}
		}
	}
	if len(state.Remaining) > 0 {
		text("Remaining", "", theme.Warning)
		limit := len(state.Remaining)
		if collapse {
			limit = min(3, limit)
		}
		for _, item := range state.Remaining[:limit] {
			text(item, "• ", theme.Text)
		}
		if limit < len(state.Remaining) {
			text(fmt.Sprintf("%d more · Ctrl+O to expand", len(state.Remaining)-limit), "", theme.TextMuted)
		}
	}
	return true
}

func taskStateAppearance(status string, theme style.UITheme) (string, string, color.Color) {
	switch status {
	case task.Completed:
		return "Completed", "✓", theme.Success
	case task.Running, "in_progress":
		return "In progress", "◌", theme.Secondary
	case task.Blocked:
		return "Blocked", "!", theme.Warning
	case task.Unverified:
		return "Needs verification", "?", theme.Warning
	case task.Failed:
		return "Failed", "✗", theme.Error
	case task.Cancelled:
		return "Cancelled", "–", theme.TextMuted
	case task.LimitReached:
		return "Limit reached", "!", theme.Warning
	case task.Unfinished:
		return "Unfinished", "○", theme.Warning
	default:
		return "Pending", "○", theme.TextMuted
	}
}
