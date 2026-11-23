package ui

import (
	"io"

	"loop/pkg/agent"
	"loop/pkg/terminal"
)

func HandleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader) {
	_, height := terminal.RefreshDimensions()
	if height <= 3 {
		return
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	uiImpl := getUI()
	uiImpl.StateMu.Lock()
	activeOp := uiImpl.ActiveCancelFunc != nil || uiImpl.State.IsGenerating
	inModal := uiImpl.InApprovalPrompt || uiImpl.InInteractiveMenu || terminal.Default().InAltScreen()
	history := uiImpl.ChatHistory
	uiImpl.StateMu.Unlock()
	if inModal {
		return
	}

	cw := crnlWriter{W: w}
	DrawConsoleStaticControlsLocked(cw, a, kiReader, !activeOp)
	if history != nil {
		history.PaintIfNeededLocked()
	}

	if !activeOp && a != nil && a.CurrentWriter() != nil {
		if pw, ok := agent.GetScreenWriter(a.CurrentWriter()).(*PromptPreservingWriter); ok {
			pw.ForceRepositionLocked()
		}
	}
}
