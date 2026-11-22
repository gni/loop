package ui

import (
	"fmt"
	"io"
	"path/filepath"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func GetPromptSymbol(_ *config.Config) string {
	return "▹ "
}

func DrawConsoleStaticControlsLocked(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, _ *term.Terminal, drawPrompt bool) {
	state := getUI()
	inAlt := terminal.Default().InAltScreen()
	state.StateMu.Lock()
	inApproval := state.InApprovalPrompt
	inMenu := state.InInteractiveMenu || inAlt
	activeOp := state.ActiveCancelFunc != nil
	state.StateMu.Unlock()
	if inMenu {
		return
	}

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
	id := a.ActiveSessionID()
	label, known := sessionDisplayLabel(a, id)
	state.StateMu.Lock()
	if state.SessionID != id || known {
		state.SessionID = id
		state.State.SessionLabel = label
	}
	state.State.Workspace = filepath.Base(a.WorkspaceRoot)
	state.StateMu.Unlock()

	live := a.LiveSettings()
	activeTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	state.StateMu.Lock()
	state.State.ShowTokens = live.ShowTokens
	if live.Model != "" {
		state.State.Model = live.Model
	}
	state.StateMu.Unlock()

	maxInputRows := 8
	if a != nil {
		a.LLMProviderMu.RLock()
		if a.Config != nil {
			maxInputRows = a.Config.PromptRows()
		}
		a.LLMProviderMu.RUnlock()
	}

	promptPrefix := GetPromptSymbol(nil)
	if kiReader != nil && kiReader.MAM != nil {
		if name := kiReader.MAM.ActiveAgentName(); name != "" {
			promptPrefix = fmt.Sprintf("[%s]%s", name, promptPrefix)
		}
	}

	qLen := 0
	if kiReader != nil {
		qLen = kiReader.QueueLen()
	}

	inputLine := ""
	posOffset := 0
	if drawPrompt && kiReader != nil {
		if activeOp {
			inputLine, posOffset = kiReader.GetTypeAheadDraft()
		} else {
			inputLine, posOffset = kiReader.GetPromptDraft()
		}
		if pasted := kiReader.GetPastedText(); pasted != "" {
			inputLine += pasted
			posOffset = len([]rune(inputLine))
		}
	}

	state.StateMu.Lock()
	oldOffset := state.PasteLinesOffset
	savedStats := state.LastStatsText
	hint := state.PromptHint
	state.StateMu.Unlock()
	render.DrawConsoleControls(w, render.ConsoleControls{
		Width: termW, Height: height, Theme: activeTheme,
		PromptPrefix: promptPrefix, QueueLength: qLen,
		InputLine: inputLine, InputPosition: posOffset, MaxInputRows: maxInputRows,
		SetCursor: func(row, col int) {
			if impl, ok := a.UI.(*AgentUIImpl); ok && impl.PPWriter != nil {
				impl.PPWriter.SetPromptPositionLocked(row, col)
			}
		},
		DrawPrompt: drawPrompt, InApproval: inApproval,
		OldPasteOffset: oldOffset, Hint: hint,
		SetPasteOffset: func(offset int) {
			state.SetPasteLinesOffset(offset)
		},
		OnScroll: func(delta int) {
			if history := state.GetChatHistory(); history != nil {
				history.resizeLocked(termW, height)
				return
			}
			if uiImpl, ok := a.UI.(*AgentUIImpl); ok && uiImpl.PPWriter != nil {
				uiImpl.PPWriter.AdjustPrintLineLocked(delta)
			} else if pw, ok := agent.GetScreenWriter(a.CurrentWriter()).(*PromptPreservingWriter); ok {
				pw.AdjustPrintLineLocked(delta)
			}
		},
		DrawFooter: func(dest io.Writer) {
			DrawStatusBarLocked(dest, activeTheme)
			DrawStaticPromptSeparatorLocked(dest, live.ShowThinking, live.ReasoningEffort, activeTheme)
			DrawStaticStatsLineLocked(dest, activeTheme, "", savedStats)
		},
	})
	if history := state.GetChatHistory(); history != nil && history.needsPaint && !history.captureOnly {
		history.paintLocked()
	}
}
