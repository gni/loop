package repl

// [STDLIB-READ]: Mapping linux-amd64 file descriptors. Architecture: Byte-Buffered state loop, Max Matrix: 80x24 Strict, Mode: Raw syscall manipulation.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func RunREPL(a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {

	ui.IsInteractive = true
	defer func() { ui.IsInteractive = false }()

	currentSessionID := initialSessionID
	if currentSessionID == "" {
		currentSessionID = db.NewUUID()
		// Persist the pointer immediately so a crash or os.Exit path still leaves
		// the session resumable via --resume; the graceful-exit write at the end of
		// RunREPL stays as the authoritative update.
		_ = db.SetLatestSessionID(currentSessionID)
	}

	var messages []db.Message
	if dbHistory, err := db.LoadMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
		messages = dbHistory
		if len(messages) > 0 && messages[0].Role == "system" {
			currentSysPrompt := a.GetSystemPrompt()
			if messages[0].Content != currentSysPrompt {
				messages[0].Content = currentSysPrompt
				_ = db.RewriteSession(currentSessionID, messages)
			}
		}
	} else {
		messages = []db.Message{
			{Role: "system", Content: a.GetSystemPrompt()},
		}
	}

	activeTasks := a.CountActiveTasks()

	ui.ClearTerminalForStartup(os.Stderr)
	ui.SetScrollRegionOffset(3)
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
		ui.RenderMCPStartupErrors(ppWriter, a.McpStartErrors, theme)
	}

	fd := int(os.Stdin.Fd())

	historyLines, _ := db.GetUserHistory()
	hist := ui.NewCustomHistory()
	for _, hLine := range historyLines {
		hist.Add(hLine)
	}

	mam := swarm.NewMultiAgentManager(a, ppWriter, theme)
	a.MultiAgentManager = mam

	a.ClearAgentsFunc = func() {
		mam.ClearAllAgents()
	}

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
	kiReader.OnClearPromptHint = func() {
		ui.GetUI().ClearPromptHint()
	}
	kiReader.OnRedrawScreen = func() {
		if kiReader.RL != nil {
			ui.RedrawScreen(kiReader.W, kiReader.Agent, kiReader, kiReader.RL)
		} else {
			ui.RedrawScreen(kiReader.W, kiReader.Agent, kiReader, nil)
		}
	}
	kiReader.OnDrawStaticControls = func(targetWriter io.Writer) {
		ui.TerminalMu.Lock()
		ui.DrawConsoleStaticControlsLocked(targetWriter, kiReader.Agent, kiReader, kiReader.RL, true)
		ui.TerminalMu.Unlock()
	}
	kiReader.OnRedrawPromptSeparator = func() {
		ui.DrawStaticPromptSeparator(kiReader.W, kiReader.Agent.Config.ShowThinking, kiReader.Agent.Config.ReasoningEffort, theme)
	}
	kiReader.OnClearCancelFunc = func() {
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
	}

	ui.GetUI().ActiveInputReader = kiReader
	defer func() {
		fmt.Fprint(os.Stderr, "\x1b[?2004l")
		ui.GetUI().ActiveInputReader = nil
	}()

	rawChan := make(chan byte, 1000)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := kiReader.R.Read(buf)
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
					close(rawChan)
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for i := 0; i < n; i++ {
				rawChan <- buf[i]
			}
		}
	}()

	go func() {
		for event := range a.SystemEvents {
			activeTheme := ui.GetConfiguredTheme(a.Config)
			ui.DrawStatusBar(os.Stderr, activeTheme)

			if event != "" && kiReader.IsAtMainPrompt {
				ui.GetUI().SetPromptHint(fmt.Sprintf("[%s]", event), 3*time.Second, nil)
			}
		}
	}()

	go startREPLRawInputDispatcher(rawChan, kiReader)

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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		var debounceTimer *time.Timer
		for range sigChan {
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
				if w, h, err := term.GetSize(fd); err == nil {
					rl.SetSize(w, h)
					ui.GetUI().StateMu.Lock()
					idle := ui.GetUI().ActiveCancelFunc == nil
					ui.GetUI().StateMu.Unlock()
					if idle {
						ui.RedrawScreen(os.Stderr, a, kiReader, rl)
					} else {
						ui.HandleResize(os.Stderr, a, kiReader, rl)
						kiReader.RedrawTypeAhead()
					}
				}
			})
		}
	}()
	defer func() {
		signal.Stop(sigChan)
	}()

	for {
		promptPrefix := ui.GetPromptSymbol(a.Config)
		if mam.ActiveAgent != nil {
			promptPrefix = fmt.Sprintf("[%s]%s", mam.ActiveAgent.Name, promptPrefix)
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

			// Redraw clean prompt prefix so submitted input does not linger on input line
			promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			fmt.Fprintf(os.Stderr, "\x1b[%d;1H\x1b[2K%s", height-2, promptStyle.Render(promptPrefix))
			ppWriter.SetPromptCol(1 + utf8.RuneCountInString(promptPrefix))
			ui.DrawStatusBar(os.Stderr, theme)
		}

		if !fromQueue {
			if kiReader.PastedText != "" {
				line = line + kiReader.PastedText
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
				ui.GetUI().StateMu.Unlock()
				kiReader.ClearQueue()
				ui.GetUI().StateMu.Lock()
				ui.GetUI().State.QueuedPromptsCount = 0
				ui.GetUI().StateMu.Unlock()
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
		line, _ = ui.NormalizeHistoryInput(line, len([]rune(line)))
		line = strings.ReplaceAll(line, "↵", "\n")
		if kiReader.PastedCodeBlocks != nil {
			for tag, code := range kiReader.PastedCodeBlocks {
				if strings.Contains(line, tag) {
					line = strings.ReplaceAll(line, tag, code)
				}
			}
			kiReader.PastedCodeBlocks = nil
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		if strings.HasPrefix(line, "/system_event ") {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		if isCmd, cmdStr := parseManualCommand(line, a.Config.DirectCommands); isCmd {
			executeManualShellCommand(
				cmdStr,
				line,
				promptPrefix,
				a,
				ppWriter,
				&messages,
				currentSessionID,
				kiReader,
				rl,
				allowedTools,
				mam,
				theme,
			)
			continue
		}

		trimmedLine := strings.TrimSpace(line)
		isHelpCmd := trimmedLine == "help" || trimmedLine == "h" || trimmedLine == "?" || trimmedLine == "/help" || trimmedLine == "/commands" || trimmedLine == "/h" || trimmedLine == "/?"
		isSlashCmd := strings.HasPrefix(trimmedLine, "/") || isHelpCmd

		var handled, quit bool
		if isSlashCmd {
			handled, quit = commands.HandleSlashCommand(a, line, &messages, allowedTools, &theme, ppWriter, &currentSessionID, rl.History, mam, kiReader)
			kiReader.Drain()

			if handled {
				if quit {
					break
				}
				continue
			}
		}

		if handled {
			if quit {
				break
			}
			ui.DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, theme)
			ui.GetUI().StateMu.Lock()
			savedStats = ui.GetUI().LastStatsText
			ui.GetUI().StateMu.Unlock()
			ui.DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
			ui.DrawStatusBar(os.Stderr, theme)
			continue
		}

		ppWriter.ForceReposition()
		renderTurnPromptHeader(ppWriter, a.Config, theme, promptPrefix, line, fromQueue, kiReader)
		executeREPLTurn(a, mam, ppWriter, &messages, line, allowedTools, theme, currentSessionID, fd, kiReader)
		refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
	}

	fmt.Fprint(os.Stderr, "\x1b[?2004l")
	ui.ShutdownStatusBar(os.Stderr)
	ui.ForceExitAlternateScreen(os.Stderr)
	_ = db.SetLatestSessionID(currentSessionID)
	fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: ./loop --session %s (or ./loop --resume)\n", currentSessionID)
}
