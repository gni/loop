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
	"loop/pkg/domain/message"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func RedrawScreen(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader) {
	RedrawScreenWithNotice(w, a, kiReader, "")
}

func RefreshConsoleAfterTurn(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if getUI().IsInModal() {
		return
	}
	DrawConsoleStaticControlsLocked(w, a, kiReader, true)
}

func RefreshConsoleAfterPromptCancellation(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader) {
	RefreshConsoleAfterTurn(w, a, kiReader)
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

func RestoreAfterModal(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader) {
	if a == nil || a.Config == nil || !modalHadTTY() {
		RedrawScreen(w, a, kiReader)
		return
	}

	terminal.RefreshDimensions()

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
	DrawConsoleStaticControlsLocked(dest, a, kiReader, true)
}

func RedrawScreenWithNotice(w io.Writer, a *agent.Agent, kiReader *keyInterceptorReader, notice string) {
	TerminalMu.Lock()
	uiImpl := getUI()
	uiImpl.StateMu.Lock()
	inModal := uiImpl.InApprovalPrompt || uiImpl.InInteractiveMenu || terminal.Default().InAltScreen()
	history := uiImpl.ChatHistory
	uiImpl.StateMu.Unlock()
	if inModal {
		TerminalMu.Unlock()
		return
	}
	if history != nil && history.RedrawLocked() {
		history.SetNoticeLocked(notice)
		uiImpl.StateMu.Lock()
		uiImpl.LastStatusBarText = ""
		uiImpl.StateMu.Unlock()
		DrawConsoleStaticControlsLocked(agent.UnwrapWriter(w), a, kiReader, true)
		TerminalMu.Unlock()
		if notice != "" {
			live := a.LiveSettings()
			noticeStyle := style.NewStyle().Foreground(style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme).Success).Italic(true)
			if hw := history.Writer(); hw != nil {
				hw.EnsureNewline()
				fmt.Fprintln(hw, noticeStyle.Render(notice))
			}
		}
		return
	}
	TerminalMu.Unlock()

	// Snapshot settings, messages, and any live stream replay into an in-memory
	// buffer BEFORE acquiring TerminalMu so replay (which takes sr.mu) can never
	// invert lock ordering with a foreground writer holding sr.mu -> TerminalMu.
	live := a.LiveSettings()
	SetCollapseStatus(live.CollapseResults)
	activeTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)

	cfgSnapshot, cfgErr := a.Configuration()
	if cfgErr != nil {
		cfgSnapshot = a.Config
	}

	var buf bytes.Buffer
	cwBuf := crnlWriter{W: &buf}

	render.RenderMCPStartupErrors(cwBuf, a.MCPStartupErrors(), activeTheme)

	var activeMessages []db.Message
	if kiReader != nil && kiReader.Messages != nil {
		a.MessagesMu.RLock()
		activeMessages = message.CloneAll(*kiReader.Messages)
		a.MessagesMu.RUnlock()
	}
	a.CurrentStreamMu.Lock()
	historySnapshot := a.CurrentHistorySnapshot
	a.CurrentStreamMu.Unlock()
	if historySnapshot != nil {
		activeMessages = historySnapshot()
	}
	if kiReader != nil && kiReader.MAM != nil {
		if activeHistory, active := kiReader.MAM.ActiveHistorySnapshot(); active {
			activeMessages = activeHistory
		}
	}

	if notice != "" {
		noticeStyle := style.NewStyle().Foreground(activeTheme.Success).Italic(true)
		fmt.Fprintln(cwBuf, noticeStyle.Render(notice))
		fmt.Fprintln(cwBuf)
	}
	if len(activeMessages) > 0 {
		PrintSessionHistory(cwBuf, activeMessages, activeTheme, cfgSnapshot)
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
		replay(cwBuf, live.CollapseResults, activeTheme)
	} else {
		// The pinned controls already reserve the activity gap. Keep a
		// completed transcript on its last occupied row instead of adding
		// empty rows for history's line ending and a second redraw newline.
		buf.Truncate(len(bytes.TrimRight(buf.Bytes(), "\r\n")))
	}

	var mam *swarm.MultiAgentManager
	if kiReader != nil {
		mam = kiReader.MAM
	}

	pTok, cTok, estimated := calculateActiveTokenUsage(a, activeMessages, activeToolAllowlist(kiReader), mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(activeMessages)
	effLimit := a.GetEffectiveContextLimit(pTok)

	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	uiImpl.StateMu.Lock()
	inModal = uiImpl.InApprovalPrompt || uiImpl.InInteractiveMenu || terminal.Default().InAltScreen()
	if inModal {
		uiImpl.StateMu.Unlock()
		return
	}
	uiImpl.LastH = 0
	pasteOffset := uiImpl.PasteLinesOffset
	generating := uiImpl.State.IsGenerating
	uiImpl.StateMu.Unlock()

	_, height := GetTerminalSize()
	scrollBottom := style.ConsoleScrollBottom(height, pasteOffset)
	var finalBuf bytes.Buffer
	cwFinal := crnlWriter{W: &finalBuf}
	fmt.Fprintf(cwFinal, "%s\x1b[r\x1b[H\x1b[J\x1b[1;%dr\x1b[1;1H", terminal.BeginSyncOutput, scrollBottom)
	finalBuf.Write(buf.Bytes())

	UpdateStatus(live.Model, pTok, cTok, latestTurnTokens, effLimit, generating, 0, a.CountActiveTasks(), live.ShowTokens, estimated)
	DrawConsoleStaticControlsLocked(render.WrapSyncFrame(cwFinal), a, kiReader, true)
	if agentUI, ok := a.UI.(*AgentUIImpl); ok && agentUI.PPWriter != nil {
		agentUI.PPWriter.ResetAfterReplayLocked(height, buf.Bytes())
	}

	if counter, ok := a.CurrentWriter().(interface{ ResetAfterReplay([]byte) }); ok {
		counter.ResetAfterReplay(buf.Bytes())
	}

	dest := w
	if _, isPP := w.(*PromptPreservingWriter); isPP {
		dest = os.Stderr
	}
	finalBuf.WriteString(terminal.EndSyncOutput)
	_, _ = dest.Write(finalBuf.Bytes())
}
