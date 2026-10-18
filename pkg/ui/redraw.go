package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func RedrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RedrawScreenWithNotice(w, a, kiReader, rl, "")
}

func RefreshConsoleAfterTurn(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	getUI().StateMu.Lock()
	inModal := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()
	if inModal {
		return
	}
	DrawConsoleStaticControlsLocked(w, a, kiReader, rl, true)
}

func RefreshConsoleAfterPromptCancellation(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RefreshConsoleAfterTurn(w, a, kiReader, rl)
}

var modalHadTTY = func() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		if term.IsTerminal(int(f.Fd())) {
			return true
		}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	return term.IsTerminal(int(tty.Fd()))
}

func RestoreAfterModal(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	if a == nil || a.Config == nil || !modalHadTTY() {
		RedrawScreen(w, a, kiReader, rl)
		return
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	uiImpl := getUI()
	uiImpl.StateMu.Lock()
	uiImpl.LastH = 0
	uiImpl.LastStatusBarText = ""
	uiImpl.StateMu.Unlock()

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	DrawConsoleStaticControlsLocked(dest, a, kiReader, rl, true)
}

func RedrawScreenWithNotice(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal, notice string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	SetCollapseStatus(a.Config.CollapseResults)
	activeTheme := GetConfiguredTheme(a.Config)

	if rl != nil {
		rl.SetPrompt("")
	}

	getUI().LastH = 0

	var buf bytes.Buffer
	cwBuf := crnlWriter{W: &buf}

	if len(a.McpStartErrors) > 0 {
		RenderMCPStartupErrors(cwBuf, a.McpStartErrors, activeTheme)
	}

	PrintBanner(cwBuf, a)
	if notice != "" {
		noticeStyle := style.NewStyle().Foreground(activeTheme.Success).Italic(true)
		fmt.Fprintln(cwBuf, noticeStyle.Render(notice))
		fmt.Fprintln(cwBuf)
	}

	var activeMessages []db.Message
	if kiReader != nil && kiReader.Messages != nil {
		activeMessages = *kiReader.Messages
	}
	if kiReader != nil && kiReader.MAM != nil && kiReader.MAM.ActiveAgent != nil {
		kiReader.MAM.ActiveAgent.HistoryMu.RLock()
		activeMessages = kiReader.MAM.ActiveAgent.History
		kiReader.MAM.ActiveAgent.HistoryMu.RUnlock()
	}

	if len(activeMessages) > 0 {
		PrintSessionHistory(cwBuf, activeMessages, activeTheme, a.Config)
	}

	_, height := GetTerminalSize()
	scrollBottom := height - 6 - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{W: &finalBuf}

	fmt.Fprintf(cwFinal, "\x1b[r\x1b[H\x1b[J\x1b[1;%dr\x1b[1;1H", scrollBottom)
	fmt.Fprint(cwFinal, buf.String())

	a.CurrentStreamMu.Lock()
	if a.CurrentStreamBuffer != nil && a.CurrentStreamBuffer.Len() > 0 {
		_, _ = cwFinal.Write(a.CurrentStreamBuffer.Bytes())
	}
	a.CurrentStreamMu.Unlock()

	var activeMessagesForTokens []db.Message
	var mam *swarm.MultiAgentManager
	if kiReader != nil {
		if kiReader.Messages != nil {
			activeMessagesForTokens = *kiReader.Messages
		}
		mam = kiReader.MAM
	}

	pTok, cTok, estimated := calculateActiveTokenUsage(a, activeMessagesForTokens, activeToolAllowlist(kiReader), mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(activeMessagesForTokens)
	effLimit := a.GetEffectiveContextLimit(pTok)

	UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, a.CountActiveTasks(), a.Config.ShowTokens, estimated)
	DrawConsoleStaticControlsLocked(cwFinal, a, kiReader, rl, true)

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	_, _ = dest.Write(finalBuf.Bytes())
}
