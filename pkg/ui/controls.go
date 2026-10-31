package ui

import (
	"fmt"
	"io"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/render"
)

func GetPromptSymbol(_ *config.Config) string {
	return "> "
}

func DrawConsoleStaticControlsLocked(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, drawPrompt bool) {
	termW, height := GetTerminalSize()
	if height <= 3 {
		return
	}
	if termW <= 0 {
		termW = 80
	}
	if impl, ok := a.UI.(*AgentUIImpl); ok && impl.PPWriter != nil {
		impl.PPWriter.ResizeLocked(height)
	}
	activeTheme := GetConfiguredTheme(a.Config)

	promptPrefix := GetPromptSymbol(a.Config)
	if kiReader != nil && kiReader.MAM != nil {
		if name := kiReader.MAM.ActiveAgentName(); name != "" {
			promptPrefix = fmt.Sprintf("[%s]%s", name, promptPrefix)
		}
	}

	qLen := 0
	if kiReader != nil {
		qLen = kiReader.QueueLen()
	}

	getUI().StateMu.Lock()
	inApproval := getUI().InApprovalPrompt
	activeOp := getUI().ActiveCancelFunc != nil
	getUI().StateMu.Unlock()

	inputLine := ""
	posOffset := 0
	if drawPrompt {
		if activeOp && kiReader != nil {
			getUI().StateMu.Lock()
			inputLine = string(kiReader.TypeAheadBuffer)
			posOffset = len([]rune(inputLine))
			getUI().StateMu.Unlock()
		} else if rl != nil {
			inputLine, posOffset = getTerminalLine(rl)
		} else if kiReader != nil {
			inputLine = kiReader.CurrentInputLine
			posOffset = kiReader.CurrentInputPos
		}
		if kiReader != nil && kiReader.PastedText != "" {
			inputLine += kiReader.PastedText
			posOffset = len([]rune(inputLine))
		}
	}

	getUI().StateMu.Lock()
	oldOffset := getUI().PasteLinesOffset
	savedStats := getUI().LastStatsText
	hint := getUI().PromptHint
	getUI().StateMu.Unlock()
	render.DrawConsoleControls(w, render.ConsoleControls{
		Width: termW, Height: height, Theme: activeTheme,
		PromptPrefix: promptPrefix, QueueLength: qLen,
		InputLine: inputLine, InputPosition: posOffset, MaxInputRows: a.Config.PromptRows(),
		SetCursor: func(row, col int) {
			if impl, ok := a.UI.(*AgentUIImpl); ok && impl.PPWriter != nil {
				impl.PPWriter.SetPromptPositionLocked(row, col)
			}
		},
		DrawPrompt: drawPrompt, InApproval: inApproval,
		OldPasteOffset: oldOffset, Hint: hint,
		SetPasteOffset: func(offset int) {
			getUI().StateMu.Lock()
			getUI().PasteLinesOffset = offset
			getUI().StateMu.Unlock()
		},
		OnScroll: func(delta int) {
			if history := getUI().ChatHistory; history != nil {
				history.resizeLocked(termW, height)
				return
			}
			if uiImpl, ok := a.UI.(*AgentUIImpl); ok && uiImpl.PPWriter != nil {
				uiImpl.PPWriter.AdjustPrintLineLocked(delta)
			} else if pw, ok := a.CurrentWriter().(*PromptPreservingWriter); ok {
				pw.AdjustPrintLineLocked(delta)
			}
		},
		DrawFooter: func(dest io.Writer) {
			DrawStatusBarLocked(dest, activeTheme)
			DrawStaticPromptSeparatorLocked(dest, a.Config.ShowThinking, a.Config.ReasoningEffort, activeTheme)
			DrawStaticStatsLineLocked(dest, activeTheme, "", savedStats)
		},
	})
	if history := getUI().ChatHistory; history != nil && history.needsPaint && !history.captureOnly {
		history.paintLocked()
	}
}
