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
	"loop/pkg/ui/render"
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
	if history := getUI().ChatHistory; history != nil && history.RedrawLocked() {
		history.notice = notice
		DrawConsoleStaticControlsLocked(agent.UnwrapWriter(w), a, kiReader, rl, true)
		TerminalMu.Unlock()
		if notice != "" {
			noticeStyle := style.NewStyle().Foreground(GetConfiguredTheme(a.Config).Success).Italic(true)
			history.writer.EnsureNewline()
			fmt.Fprintln(history.writer, noticeStyle.Render(notice))
		}
		return
	}
	defer TerminalMu.Unlock()

	SetCollapseStatus(a.Config.CollapseResults)
	activeTheme := GetConfiguredTheme(a.Config)

	getUI().LastH = 0

	var buf bytes.Buffer
	cwBuf := crnlWriter{W: &buf}

	if len(a.McpStartErrors) > 0 {
		render.RenderMCPStartupErrors(cwBuf, a.McpStartErrors, activeTheme)
	}

	var activeMessages []db.Message
	if kiReader != nil && kiReader.Messages != nil {
		activeMessages = *kiReader.Messages
	}
	a.CurrentStreamMu.Lock()
	historySnapshot := a.CurrentHistorySnapshot
	a.CurrentStreamMu.Unlock()
	if historySnapshot != nil {
		activeMessages = historySnapshot()
	}
	if kiReader != nil && kiReader.MAM != nil {
		if history, active := kiReader.MAM.ActiveHistorySnapshot(); active {
			activeMessages = history
		}
	}

	PrintBanner(cwBuf, a)
	if notice != "" {
		noticeStyle := style.NewStyle().Foreground(activeTheme.Success).Italic(true)
		fmt.Fprintln(cwBuf, noticeStyle.Render(notice))
		fmt.Fprintln(cwBuf)
	}
	if len(activeMessages) > 0 {
		PrintSessionHistory(cwBuf, activeMessages, activeTheme, a.Config)
	}

	a.CurrentStreamMu.Lock()
	replay := a.CurrentStreamReplay
	if replay == nil && a.CurrentStreamBuffer != nil && a.CurrentStreamBuffer.Len() > 0 {
		_, _ = cwBuf.Write(a.CurrentStreamBuffer.Bytes())
	}
	a.CurrentStreamMu.Unlock()
	if replay != nil {
		if len(activeMessages) > 0 && activeMessages[len(activeMessages)-1].Role == "user" {
			fmt.Fprintln(cwBuf, style.ActivityDivider(activeTheme))
		}
		replay(cwBuf, a.Config.CollapseResults, activeTheme)
	} else {
		// The pinned controls already reserve the activity gap. Keep a
		// completed transcript on its last occupied row instead of adding
		// empty rows for history's line ending and a second redraw newline.
		buf.Truncate(len(bytes.TrimRight(buf.Bytes(), "\r\n")))
	}
	_, height := GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, getUI().PasteLinesOffset)
	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{W: &finalBuf}
	fmt.Fprintf(cwFinal, "\x1b[?2026h\x1b[r\x1b[H\x1b[J\x1b[1;%dr\x1b[1;1H", scrollBottom)
	finalBuf.Write(buf.Bytes())

	var mam *swarm.MultiAgentManager
	if kiReader != nil {
		mam = kiReader.MAM
	}

	pTok, cTok, estimated := calculateActiveTokenUsage(a, activeMessages, activeToolAllowlist(kiReader), mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(activeMessages)
	effLimit := a.GetEffectiveContextLimit(pTok)

	getUI().StateMu.Lock()
	generating := getUI().State.IsGenerating
	getUI().StateMu.Unlock()
	UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, generating, 0, a.CountActiveTasks(), a.Config.ShowTokens, estimated)
	DrawConsoleStaticControlsLocked(cwFinal, a, kiReader, rl, true)
	if uiImpl, ok := a.UI.(*AgentUIImpl); ok && uiImpl.PPWriter != nil {
		uiImpl.PPWriter.ResetAfterReplayLocked(height, buf.Bytes())
	}

	if counter, ok := a.CurrentWriter().(interface{ ResetAfterReplay([]byte) }); ok {
		counter.ResetAfterReplay(buf.Bytes())
	}

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	finalBuf.WriteString("\x1b[?2026l")
	_, _ = dest.Write(finalBuf.Bytes())
}
