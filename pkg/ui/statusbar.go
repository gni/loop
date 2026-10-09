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
	getUI().StateMu.Lock()
	getUI().State.PlanCompleted = completed
	getUI().State.PlanTotal = total
	getUI().StateMu.Unlock()
}

func UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool, tokenEstimate ...bool) {
	estimated := isGenerating
	if len(tokenEstimate) > 0 {
		estimated = tokenEstimate[0]
	}

	getUI().StateMu.Lock()
	getUI().State.Model = model
	if promptTokens >= 0 {
		getUI().State.PromptTokens = promptTokens
	}
	if completionTokens >= 0 {
		getUI().State.CompletionTokens = completionTokens
	}
	getUI().State.CurrentCompletionTokens = currentCompletionTokens
	getUI().State.ContextLimit = contextLimit
	getUI().State.IsGenerating = isGenerating
	getUI().State.TokenEstimate = estimated
	getUI().State.ActiveTasksCount = activeTasks
	getUI().State.ShowTokens = showTokens
	if tps > 0 {
		getUI().State.LastTps = tps
		getUI().State.HasLastTps = true
	} else if !isGenerating {
		getUI().State.HasLastTps = false
		getUI().State.LastTps = 0
	}
	getUI().StateMu.Unlock()
}

func DrawStatusBar(w io.Writer, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStatusBarLocked(w, theme)
}

func DrawStatusBarLocked(w io.Writer, theme UITheme) {
	getUI().StateMu.Lock()
	defer getUI().StateMu.Unlock()

	if !getUI().Enabled {
		return
	}

	width, height := GetTerminalSize()
	if height <= 3 {
		return
	}

	var buf bytes.Buffer

	offset := getUI().ScrollRegionOffset
	scrollBottom := height - 2 - offset - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}
	if height != getUI().LastH || getUI().PasteLinesOffset != getUI().LastPasteLinesOffset {
		getUI().LastH = height
		getUI().LastPasteLinesOffset = getUI().PasteLinesOffset
		getUI().LastStatusBarText = "" // Force redraw of the actual text
	}
	fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8", scrollBottom)

	// Save cursor
	fmt.Fprint(&buf, "\x1b7")

	newStatusBarText := render.FormatStatusBarLine(getUI().State, theme, width)

	indicator := "▾"
	if getUI().CollapseResults {
		indicator = "›"
	}
	deltaKey := newStatusBarText + indicator

	// Line-Level Delta Rendering check
	if deltaKey == getUI().LastStatusBarText && height == getUI().LastH {
		return
	}
	getUI().LastStatusBarText = deltaKey

	// Save cursor
	buf.WriteString("\x1b[?2026h\x1b7")

	// Draw separator line at height-1
	fmt.Fprintf(&buf, "\x1b[%d;1H", height-1)
	fmt.Fprint(&buf, "\x1b[2K")
	borderLine := render.FormatStatusBarSeparator(width, indicator, theme)
	fmt.Fprint(&buf, borderLine)

	// Draw status bar content at height
	fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	fmt.Fprint(&buf, "\x1b[2K")

	fmt.Fprint(&buf, newStatusBarText)

	// Restore cursor
	buf.WriteString("\x1b8\x1b[?2026l")

	_, _ = w.Write(buf.Bytes())
}

func formatLeft(theme UITheme, width int) string {
	return render.FormatStatusBarLeft(getUI().State, theme, width)
}

func formatRight(theme UITheme, width int) string {
	return render.FormatStatusBarRight(getUI().State, theme, width)
}
