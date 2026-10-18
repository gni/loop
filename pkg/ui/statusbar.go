package ui

import (
	"bytes"
	"fmt"
	"io"

	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

type StatusBarState = render.StatusBarState

func GetTerminalSize() (int, int) {
	return style.GetTerminalSize()
}

func UpdatePlanStatus(completed, total int) {
	ui := getUI()
	ui.StateMu.Lock()
	ui.State.PlanCompleted = completed
	ui.State.PlanTotal = total
	ui.StateMu.Unlock()
}

func UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool, tokenEstimate ...bool) {
	estimated := isGenerating
	if len(tokenEstimate) > 0 {
		estimated = tokenEstimate[0]
	}

	ui := getUI()
	ui.StateMu.Lock()
	ui.State.Model = model
	if promptTokens >= 0 {
		ui.State.PromptTokens = promptTokens
	}
	if completionTokens >= 0 {
		ui.State.CompletionTokens = completionTokens
	}
	ui.State.CurrentCompletionTokens = currentCompletionTokens
	ui.State.ContextLimit = contextLimit
	ui.State.IsGenerating = isGenerating
	ui.State.TokenEstimate = estimated
	ui.State.ActiveTasksCount = activeTasks
	ui.State.ShowTokens = showTokens
	if tps > 0 {
		ui.State.LastTps = tps
		ui.State.HasLastTps = true
	} else if !isGenerating {
		ui.State.HasLastTps = false
		ui.State.LastTps = 0
	}
	ui.StateMu.Unlock()
}

func DrawStatusBar(w io.Writer, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStatusBarLocked(w, theme)
}

func DrawStatusBarLocked(w io.Writer, theme UITheme) {
	ui := getUI()
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()

	if !ui.Enabled || ui.InApprovalPrompt {
		return
	}

	width, height := GetTerminalSize()
	if height <= 3 {
		return
	}

	scrollBottom := style.ConsoleScrollBottom(height, ui.PasteLinesOffset)
	if height != ui.LastH || ui.PasteLinesOffset != ui.LastPasteLinesOffset {
		ui.LastH = height
		ui.LastPasteLinesOffset = ui.PasteLinesOffset
		ui.LastStatusBarText = ""
	}

	newStatusBarText := render.FormatStatusBarLine(ui.State, theme, width)
	indicator := "▾"
	if ui.CollapseResults {
		indicator = "›"
	}
	deltaKey := newStatusBarText + indicator

	if deltaKey == ui.LastStatusBarText && height == ui.LastH {
		return
	}
	ui.LastStatusBarText = deltaKey

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8\x1b[?2026h\x1b7", scrollBottom)
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s", height-1, render.FormatStatusBarSeparator(width, indicator, theme))
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s", height, newStatusBarText)
	buf.WriteString("\x1b8\x1b[?2026l")

	_, _ = w.Write(buf.Bytes())
}
