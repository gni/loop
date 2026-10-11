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

// RedrawScreen clears and repaints the entire terminal view.
func RedrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RedrawScreenWithNotice(w, a, kiReader, rl, "")
}

func redrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RedrawScreen(w, a, kiReader, rl)
}

// RefreshConsoleAfterTurn repaints the fixed bottom controls after turn completion.
func RefreshConsoleAfterTurn(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawConsoleStaticControlsLocked(w, a, kiReader, rl, true)
}

func refreshConsoleAfterTurn(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RefreshConsoleAfterTurn(w, a, kiReader, rl)
}

// RefreshConsoleAfterPromptCancellation redraws only fixed bottom controls.
func RefreshConsoleAfterPromptCancellation(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, rl *term.Terminal) {
	RefreshConsoleAfterTurn(w, a, kiReader, rl)
}

// RestoreAfterModal repaints only the fixed bottom controls after a full-screen modal
// (the config, provider, and mcp editors). Those editors draw on the alternate screen
// buffer, so the conversation is still exactly where it was in the primary buffer; a
// clear-and-repaint from row 1 would move the chat to the top of the screen for no
// reason. The full repaint is only needed when the modal could not use the alternate
// screen, because then its output landed on the primary buffer.
// modalHadTTY is a var so tests can pin the branch without a real terminal.
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
	// The modals enter the alternate screen whenever they have a TTY: they check stdin,
	// and they fall back to /dev/tty for both input and output. Only when none of those
	// is a terminal does their output land on the primary buffer, which then needs the
	// full repaint. The check runs before taking TerminalMu because RedrawScreen takes it
	// too and the mutex is not reentrant.
	if a == nil || a.Config == nil || !modalHadTTY() {
		RedrawScreen(w, a, kiReader, rl)
		return
	}

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	// The status bar uses line-level delta rendering: it returns early when its text
	// matches LastStatusBarText and the height is unchanged. The modal erased those rows
	// without touching the cache, so an unchanged bar would be skipped and the separator
	// plus the token line would never come back. Invalidate before repainting.
	uiImpl := getUI()
	uiImpl.StateMu.Lock()
	uiImpl.LastH = 0
	uiImpl.LastStatusBarText = ""
	uiImpl.StateMu.Unlock()

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	drawConsoleStaticControlsLocked(dest, a, kiReader, rl, true)
}

// RedrawScreenWithNotice repaints the full terminal with an optional status notice.
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

	activeMessages := []db.Message{}
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
	scrollBottom := height - 5 - getUI().PasteLinesOffset
	if scrollBottom < 1 {
		scrollBottom = 1
	}

	content := buf.String()

	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{W: &finalBuf}

	// Write clear-screen first
	fmt.Fprint(cwFinal, "\x1b[r\x1b[H\x1b[J")

	// Set scroll region BEFORE rendering content so text auto-scrolls within 1..scrollBottom
	fmt.Fprintf(cwFinal, "\x1b[1;%dr\x1b[1;1H", scrollBottom)

	fmt.Fprint(cwFinal, content)

	a.CurrentStreamMu.Lock()
	if a.CurrentStreamBuffer != nil {
		b := a.CurrentStreamBuffer.Bytes()
		if len(b) > 0 {
			cwFinal.Write(b)
		}
	}
	a.CurrentStreamMu.Unlock()

	activeTasks := 0
	for _, t := range a.ListTasks() {
		if t.Status == "running" {
			activeTasks++
		}
	}

	activeMessagesForTokens := []db.Message{}
	if kiReader != nil && kiReader.Messages != nil {
		activeMessagesForTokens = *kiReader.Messages
	}
	var mam *swarm.MultiAgentManager
	if kiReader != nil {
		mam = kiReader.MAM
	}
	pTok, cTok, estimated := calculateActiveTokenUsage(a, activeMessagesForTokens, activeToolAllowlist(kiReader), mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(activeMessagesForTokens)
	effLimit := a.GetEffectiveContextLimit(pTok)

	UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)

	drawConsoleStaticControlsLocked(cwFinal, a, kiReader, rl, true)

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	_, _ = dest.Write(finalBuf.Bytes())
}
