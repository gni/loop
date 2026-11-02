package ui

import (
	"io"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/terminal"
)

func HandleResize(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	width, height := GetTerminalSize()
	if height <= 3 {
		return
	}

	if rl != nil {
		_ = rl.SetSize(width, height)
	}

	getUI().StateMu.Lock()
	activeOp := getUI().ActiveCancelFunc != nil || getUI().State.IsGenerating
	inModal := getUI().InApprovalPrompt || getUI().InInteractiveMenu || terminal.Default().InAltScreen()
	getUI().StateMu.Unlock()
	if inModal {
		return
	}

	cw := crnlWriter{W: w}
	DrawConsoleStaticControlsLocked(cw, a, kiReader, rl, !activeOp)
	if history := getUI().ChatHistory; history != nil {
		history.paintLocked()
	}

	if !activeOp && a.CurrentWriter() != nil {
		if pw, ok := a.CurrentWriter().(*PromptPreservingWriter); ok {
			pw.ForceRepositionLocked()
		} else if fr, ok := a.CurrentWriter().(interface{ ForceReposition() }); ok {
			fr.ForceReposition()
		}
	}
}
