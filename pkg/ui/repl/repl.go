package repl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func RunREPL(a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {
	ui.IsInteractive = true
	defer func() { ui.IsInteractive = false }()

	currentSessionID := initialSessionID
	if a.Ephemeral {
		currentSessionID = ""
	}
	if currentSessionID == "" && !a.Ephemeral {
		currentSessionID = db.NewUUID()
		_ = db.SetLatestSessionID(currentSessionID)
	}

	var messages []db.Message
	if dbHistory, err := a.LoadSessionMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
	}
	a.SyncSessionSystemPrompt(&messages, currentSessionID)

	activeTasks := a.CountActiveTasks()

	render.ClearTerminalForStartup(os.Stderr)
	ui.SetScrollRegionOffset(style.ConsoleScrollRegionOffset())
	ui.InitStatusBar(os.Stderr)
	defer ui.ShutdownStatusBar(os.Stderr)
	defer ui.ForceExitAlternateScreen(os.Stderr)
	defer fmt.Fprint(os.Stderr, "\x1b[?25h\x1b[?2004l")

	_, height := ui.GetTerminalSize()
	ppWriter := ui.NewPromptPreservingWriter(os.Stderr, height)
	if uiImpl, ok := a.UI.(*ui.AgentUIImpl); ok {
		uiImpl.PPWriter = ppWriter
	}

	if len(a.McpStartErrors) > 0 {
		render.RenderMCPStartupErrors(ppWriter, a.McpStartErrors, theme)
	}

	fd := int(os.Stdin.Fd())

	var historyLines []string
	if !a.Ephemeral {
		historyLines, _ = db.GetUserHistory()
	}
	hist := ui.NewCustomHistory()
	for _, hLine := range historyLines {
		hist.Add(hLine)
	}

	mam, _ := a.MultiAgentManager.(*swarm.MultiAgentManager)
	if mam == nil {
		mam = swarm.NewMultiAgentManager(a, ppWriter, theme)
	}
	mam.SetOutput(ppWriter, theme)
	a.MultiAgentManager = mam
	a.ClearAgentsFunc = func() { mam.ClearAllAgents() }
	_ = mam.LoadSavedAgents()

	kiReader := &interceptor.KeyInterceptorReader{
		R:            os.Stdin,
		Agent:        a,
		Theme:        theme,
		W:            os.Stderr,
		Messages:     &messages,
		MAM:          mam,
		AllowedTools: allowedTools,
		InputChan:    make(chan byte, 1000),
		ApprovalChan: make(chan byte, 1000),
		InjectChan:   make(chan byte, 100),
		Hist:         hist,
		HistoryIndex: -1,
	}
	configureREPLCallbacks(kiReader, &theme)

	ui.GetUI().ActiveInputReader = kiReader
	defer func() {
		fmt.Fprint(os.Stderr, "\x1b[?2004l")
		ui.GetUI().ActiveInputReader = nil
	}()

	startREPLSystemNotifications(a, kiReader)
	go startREPLRawInputDispatcher(startREPLInputReader(kiReader), kiReader)

	createTerminal := func() *term.Terminal {
		t := term.NewTerminal(kiReader, "")
		kiReader.RL = t
		t.History = hist
		if w, h, err := term.GetSize(fd); err == nil {
			t.SetSize(w, h)
		}
		t.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
			kiReader.CurrentInputLine = line
			kiReader.CurrentInputPos = pos
			return autoCompleteCallback(line, pos, key, a)
		}
		return t
	}

	rl := createTerminal()

	initialPromptTokens, initialCompletionTokens, initialTokensEstimated := interceptor.CalculateActiveTokenUsage(a, messages, allowedTools, mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(messages)

	ui.PrintBanner(ppWriter, a)
	if len(messages) > 1 {
		ui.PrintSessionHistory(ppWriter, messages, theme, a.Config)
	}

	ui.SetCollapseStatus(a.Config.CollapseResults)
	initialEffLimit := a.GetEffectiveContextLimit(initialPromptTokens)
	ui.UpdateStatus(a.Config.Model, initialPromptTokens, initialCompletionTokens, latestTurnTokens, initialEffLimit, false, 0, activeTasks, a.Config.ShowTokens, initialTokensEstimated)
	ui.DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
	ui.GetUI().StateMu.Lock()
	savedStats := ui.GetUI().LastStatsText
	ui.GetUI().StateMu.Unlock()
	ui.DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
	ui.DrawStatusBar(os.Stderr, theme)

	defer watchREPLResizes(a, kiReader, rl, fd)()

	for {
		promptPrefix := ui.GetPromptSymbol(a.Config)
		if name := mam.ActiveAgentName(); name != "" {
			promptPrefix = fmt.Sprintf("[%s]%s", name, promptPrefix)
		}

		var line string
		var err error
		fromQueue := false

		if queued, ok := kiReader.DequeuePrompt(); ok {
			line = queued
			fromQueue = true
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = kiReader.QueueLen()
			ui.GetUI().StateMu.Unlock()
			ui.DrawStatusBar(os.Stderr, theme)
		} else {
			kiReader.Drain()

			ui.GetUI().StateMu.Lock()
			uncommitted := append([]byte(nil), kiReader.TypeAheadBuffer...)
			kiReader.ResetTypeAheadLocked()
			ui.GetUI().StateMu.Unlock()
			for _, b := range uncommitted {
				kiReader.InjectChan <- b
			}

			ppWriter.SetPromptCol(1 + utf8.RuneCountInString(promptPrefix))
			rl.SetPrompt("")
			ui.TerminalMu.Lock()
			ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, rl, true)
			ui.TerminalMu.Unlock()

			fmt.Fprint(os.Stderr, "\x1b[?25h")
			ppWriter.SetCursorHidden(false)

			oldState, errRaw := term.MakeRaw(fd)
			if errRaw != nil {
				fmt.Printf("Error setting terminal raw mode: %v\n", errRaw)
				os.Exit(1)
			}

			fmt.Fprint(os.Stderr, "\x1b[?2004h")
			kiReader.IsAtMainPrompt = true
			line, err = rl.ReadLine()
			kiReader.IsAtMainPrompt = false
			fmt.Fprint(os.Stderr, "\x1b[?2004l")
			term.Restore(fd, oldState)
		}

		prevOffset := ui.GetUI().PasteLinesOffset
		ui.GetUI().StateMu.Lock()
		ui.GetUI().PasteLinesOffset = 0
		ui.GetUI().StateMu.Unlock()

		if _, curH := ui.GetTerminalSize(); curH > 0 {
			height = curH
		}

		if height > 0 {
			if prevOffset > 0 {
				for r := height - 4 - prevOffset; r <= height-2; r++ {
					if r >= 1 {
						fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K", r)
					}
				}
				ui.GetUI().StateMu.Lock()
				savedStats := ui.GetUI().LastStatsText
				ui.GetUI().StateMu.Unlock()
				ui.DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
				ui.DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			} else {
				fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K", height-2)
			}

			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K%s", height-2, promptStyle.Render(promptPrefix))
			ppWriter.SetPromptCol(1 + utf8.RuneCountInString(promptPrefix))
			ui.DrawStatusBar(os.Stderr, theme)
		}

		if !fromQueue {
			if kiReader.PastedText != "" {
				line += kiReader.PastedText
				kiReader.PastedText = ""
				ui.GetUI().PasteLinesOffset = 0
				ui.InitStatusBar(os.Stderr)
			}

			if kiReader.CtrlCInterrupted {
				ui.GetUI().ClearPromptHint()
				kiReader.CtrlCInterrupted = false
				kiReader.CurrentInputLine = ""
				kiReader.PastedText = ""
				kiReader.PastedCodeBlocks = nil
				ui.GetUI().StateMu.Lock()
				kiReader.ResetTypeAheadLocked()
				ui.GetUI().PasteLinesOffset = 0
				ui.GetUI().State.QueuedPromptsCount = 0
				ui.GetUI().StateMu.Unlock()
				kiReader.ClearQueue()
				kiReader.Drain()
				refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, true)
				continue
			}

			if err != nil {
				isEOF := err == io.EOF || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF")
				if isEOF && term.IsTerminal(fd) {
					if ui.GetUI().CheckCtrlDConfirmation(3 * time.Second) {
						break
					}
					rl = createTerminal()
					ui.GetUI().SetPromptHint("(Press Ctrl+D again to exit)", 3*time.Second, func() {
						ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader, rl)
					})
					continue
				}
				break
			}
		}

		ui.GetUI().ClearPromptHint()
		kiReader.CurrentInputLine = ""
		line = hist.GetFull(line)
		line, _ = render.NormalizeHistoryInput(line, len([]rune(line)))
		line = strings.ReplaceAll(line, "↵", "\n")
		if kiReader.PastedCodeBlocks != nil {
			for tag, code := range kiReader.PastedCodeBlocks {
				if strings.Contains(line, tag) {
					line = strings.ReplaceAll(line, tag, code)
				}
			}
			kiReader.PastedCodeBlocks = nil
		}

		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(line, "/system_event ") {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		if isCmd, cmdStr := parseManualCommand(line, a.Config.DirectCommands); isCmd {
			ui.TerminalMu.Lock()
			ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, rl, false)
			ui.TerminalMu.Unlock()
			executeManualShellCommand(cmdStr, line, promptPrefix, a, ppWriter, &messages, currentSessionID, kiReader, theme)
			rl = createTerminal()
			refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
			continue
		}

		line, handled, quit := dispatchREPLSlashCommand(a, line, &messages, allowedTools, &theme, ppWriter, &currentSessionID, rl, mam, kiReader)
		if handled {
			if quit {
				break
			}
			continue
		}

		kiReader.ActiveTurn.Store(true)
		collapseRedrawn := false
		// Consume redraw requests on the foreground renderer so committed
		// history and the current inference form one consistent frame.
		a.OnStreamProgress = func() bool {
			ui.GetUI().StateMu.Lock()
			inModal := ui.GetUI().InApprovalPrompt
			ui.GetUI().StateMu.Unlock()
			if inModal || !kiReader.PendingCollapseRedraw.Swap(false) {
				return false
			}
			ui.RedrawScreen(os.Stderr, a, kiReader, rl)
			collapseRedrawn = true
			return true
		}
		ppWriter.BeginScrollEntry()
		for _, previous := range messages {
			if previous.Role != "system" {
				ppWriter.EnsureActivityGap()
				break
			}
		}
		renderTurnPromptHeader(ppWriter, a.Config, theme, promptPrefix, line, fromQueue, kiReader)
		executeREPLTurn(a, mam, ppWriter, &messages, line, allowedTools, theme, currentSessionID, fd, kiReader)
		a.OnStreamProgress = nil
		kiReader.ActiveTurn.Store(false)
		// Both a deferred Ctrl+O and an unsafe live syntax repaint require
		// one authoritative replay, after tool calls and text reach history.
		redrawCollapse := kiReader.PendingCollapseRedraw.Swap(false)
		redrawSyntax := ppWriter.ConsumeSyntaxRefresh()
		if collapseRedrawn || redrawCollapse || redrawSyntax {
			ui.RedrawScreen(os.Stderr, a, kiReader, rl)
		}
		refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
	}

	fmt.Fprint(os.Stderr, "\x1b[?2004l")
	ui.ShutdownStatusBar(os.Stderr)
	ui.ForceExitAlternateScreen(os.Stderr)
	if !a.Ephemeral {
		_ = db.SetLatestSessionID(currentSessionID)
		fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: loop --session %s (or loop --resume)\n", currentSessionID)
	}
}
