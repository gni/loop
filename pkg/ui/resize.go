package ui

import (
	"io"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/terminal"
)

func HandleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	width, height := terminal.RefreshDimensions()
	if height <= 3 {
		return
	}

	if rl == nil && kiReader != nil {
		rl = kiReader.GetRL()
	}
	if rl != nil {
		_ = rl.SetSize(width, height)
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
	DrawConsoleStaticControlsLocked(cw, a, kiReader, rl, !activeOp)
	if history != nil {
		history.PaintIfNeededLocked()
	}

	if !activeOp && a != nil && a.CurrentWriter() != nil {
		if pw, ok := agent.GetScreenWriter(a.CurrentWriter()).(*PromptPreservingWriter); ok {
			pw.ForceRepositionLocked()
		}
	}
}
