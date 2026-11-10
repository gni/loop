package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func RunREPL(a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {
	ctx := a.Lifetime
	if ctx == nil {
		ctx = context.Background()
	}
	RunREPLContext(ctx, a, allowedTools, theme, initialSessionID)
}

func RunREPLContext(ctx context.Context, a *agent.Agent, allowedTools []string, theme style.UITheme, initialSessionID string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if a.CancelLifetime != nil {
		stop := context.AfterFunc(ctx, a.CancelLifetime)
		defer stop()
	}
	if a.SyncSessionState != nil {
		allowedTools = slices.Clone(a.ToolSelection)
	}

	ui.IsInteractive = true
	defer func() { ui.IsInteractive = false }()

	currentSessionID := initialSessionID
	shell := &manualShell{directory: a.GetWorkspaceRoot()}
	if a.Ephemeral {
		currentSessionID = ""
	}
	if currentSessionID == "" && !a.Ephemeral {
		currentSessionID = db.NewUUID()
		_ = db.SetLatestSessionIDContext(a.Context(), currentSessionID)
	}

	var messages []db.Message
	if snap, ok := a.SessionMessagesSnapshot(); ok && len(snap) > 0 {
		messages = snap
	} else {
		if dbHistory, err := a.LoadSessionMessages(currentSessionID); err == nil && len(dbHistory) > 0 {
			messages = dbHistory
		}
		a.SyncSessionSystemPrompt(&messages, currentSessionID)
	}

	activeTasks := a.CountActiveTasks()

	render.ClearTerminalForStartup(os.Stderr)
	ui.SetScrollRegionOffset(style.ConsoleScrollRegionOffset())
	ui.InitStatusBar(os.Stderr)
	a.RefreshPlanStatus()
	// Registered before history and resize cleanup so they stop drawing first.
	defer func() {
		cancel()
		ui.ShutdownStatusBar(os.Stderr)
		ui.TerminalMu.Lock()
		defer ui.TerminalMu.Unlock()
		ui.ForceExitAlternateScreen(os.Stderr)
		fmt.Fprint(os.Stderr, "\x1b[?2026l\x1b[<u\x1b[>4m\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[r\x1b[0m\x1b[?7h\x1b[?25h\x1b[2J\x1b[H")
		if !a.Ephemeral {
			fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: loop --session %s (or loop --resume)\r\n", render.SanitizeTerminalText(currentSessionID))
		}
	}()
	// Request modified Enter reporting where supported; legacy terminals ignore
	// these sequences and retain Ctrl+N/Alt+Enter/Ctrl+J as newline shortcuts.
	fmt.Fprint(os.Stderr, "\x1b[>4;2m\x1b[>1u")

	_, height := ui.GetTerminalSize()
	ppWriter := ui.NewPromptPreservingWriter(os.Stderr, height)
	if uiImpl, ok := a.UI.(*ui.AgentUIImpl); ok {
		uiImpl.PPWriter = ppWriter
	}

	fd := int(os.Stdin.Fd())

	hist := ui.NewCustomHistory()

	mam, hadMAM := a.MultiAgentManager.(*swarm.MultiAgentManager)
	if mam == nil {
		mam = swarm.NewMultiAgentManager(a, ppWriter, theme)
	}
	mam.SetOutput(ppWriter, theme)
	a.MultiAgentManager = mam
	a.ClearAgentsFunc = mam.ClearAllAgents
	if !hadMAM {
		_ = mam.LoadSavedAgents()
	}

	kiReader := &interceptor.KeyInterceptorReader{
		InputContext: ctx,
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
	chatHistory, historyErr := ui.NewChatHistory(a, kiReader, ppWriter, currentSessionID, messages)
	if historyErr != nil {
		fmt.Fprintf(os.Stderr, "Could not load older display history: %v\n", historyErr)
	}
	defer chatHistory.Close()
	kiReader.OnChatHistoryInput = chatHistory.FilterInput

	ui.GetUI().ActiveInputReader = kiReader
	defer func() {
		ui.GetUI().ActiveInputReader = nil
	}()
	restorePaste := terminal.Default().BeginBracketedPaste(os.Stderr)
	defer restorePaste()

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
			pos = min(max(0, pos), len(line))
			kiReader.SetPromptDraft(line, utf8.RuneCountInString(line[:pos]))
			if newLine, newPos, handled := handlePromptEditKey(line, pos, key, a, hist, kiReader); handled {
				newPos = min(max(0, newPos), len(newLine))
				kiReader.SetPromptDraft(newLine, utf8.RuneCountInString(newLine[:newPos]))
				return newLine, newPos, true
			}
			newLine, newPos, completed := autoCompleteCallback(line, pos, key, a)
			if completed {
				newPos = min(max(0, newPos), len(newLine))
				kiReader.SetPromptDraft(newLine, utf8.RuneCountInString(newLine[:newPos]))
			}
			return newLine, newPos, completed
		}
		return t
	}

	rl := createTerminal()

	initialPromptTokens, initialCompletionTokens, initialTokensEstimated := interceptor.CalculateActiveTokenUsage(a, messages, allowedTools, mam)
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(messages)

	render.RenderMCPStartupErrors(ppWriter, a.MCPStartupErrors(), theme)
	chatHistory.PrintInitialHistory()
	if !a.Ephemeral {
		go func() {
			historyLines, err := db.GetUserHistoryContext(a.Context())
			if err == nil && ctx.Err() == nil {
				hist.RestoreOlder(historyLines)
			}
		}()
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
		if ctx.Err() != nil {
			break
		}
		promptPrefix := ui.GetPromptSymbol(a.Config)
		if name := mam.ActiveAgentName(); name != "" {
			promptPrefix = fmt.Sprintf("[%s]%s", name, promptPrefix)
		}

		var line string
		var err error
		var attachmentSource *string
		fromQueue := false

		if queued, ok := kiReader.DequeuePrompt(); ok {
			line = queued.Text
			kiReader.PastedLiterals = queued.Pastes
			fromQueue = true
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = kiReader.QueueLen()
			ui.GetUI().StateMu.Unlock()
			ui.DrawStatusBar(os.Stderr, theme)
		} else {
			kiReader.Drain()

			ui.GetUI().StateMu.Lock()
			uncommitted := append([]byte(nil), kiReader.TypeAheadBuffer...)
			pastes := append([]string(nil), kiReader.TypeAheadPastes...)
			kiReader.ResetTypeAheadLocked()
			ui.GetUI().StateMu.Unlock()
			// A draft already belongs to the editor, not to paste detection.
			// Feed it before live keys without blocking on the injection channel
			// or turning a following Enter into part of a multiline paste.
			kiReader.SetPromptDraft("", 0)
			if len(uncommitted) > 0 {
				kiReader.PastedLiterals = pastes
				kiReader.PasteRemaining = append(kiReader.QueuePromptPaste(string(uncommitted)), kiReader.PasteRemaining...)
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

			kiReader.IsAtMainPrompt.Store(true)
			line, err = rl.ReadLine()
			kiReader.IsAtMainPrompt.Store(false)
			kiReader.SetPromptDraft("", 0)
			term.Restore(fd, oldState)
			if ctx.Err() != nil {
				break
			}
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
				kiReader.SetPromptDraft("", 0)
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
		kiReader.SetPromptDraft("", 0)
		line = hist.GetFull(line)
		commandLine := line
		if source, pasted := interceptor.PastedInputSource(line, kiReader.PastedLiterals); pasted {
			attachmentSource = &source
			commandLine = source
		}
		if kiReader.PastedCodeBlocks != nil {
			var replacements []string
			for tag, code := range kiReader.PastedCodeBlocks {
				if strings.Contains(line, tag) {
					replacements = append(replacements, tag, code)
				}
			}
			line = strings.NewReplacer(replacements...).Replace(line)
			kiReader.PastedCodeBlocks = nil
		}

		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(commandLine, "/system_event ") {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		if isCmd, _ := parseManualCommand(commandLine, a.Config.DirectCommands); isCmd {
			_, cmdStr := parseManualCommand(line, a.Config.DirectCommands)
			ui.TerminalMu.Lock()
			ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, rl, false)
			ui.TerminalMu.Unlock()
			shell.execute(cmdStr, line, promptPrefix, a, ppWriter, &messages, currentSessionID, kiReader, theme)
			rl = createTerminal()
			refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
			continue
		}

		preExpanded, handled, quit := false, false, false
		prevSessionID := currentSessionID
		_ = ui.ConsumeSessionReloaded()
		if attachmentSource == nil || strings.HasPrefix(strings.TrimSpace(commandLine), "/") || commands.IsHelpCommand(commandLine) {
			line, preExpanded, handled, quit = dispatchREPLSlashCommand(a, line, &messages, allowedTools, &theme, ppWriter, &currentSessionID, rl, mam, kiReader)
		}
		if handled {
			if quit {
				break
			}
			if ui.ConsumeSessionReloaded() {
				if a.SyncSessionState != nil {
					if err := a.SyncSessionState(messages, a.ToolSelection, currentSessionID, false); err != nil {
						fmt.Fprintf(ppWriter, "Could not save session state: %v\n", err)
					}
					theme = ui.GetConfiguredTheme(a.Config)
					kiReader.Theme = theme
					allowedTools = slices.Clone(a.ToolSelection)
					kiReader.AllowedTools = allowedTools
				}
				if err := chatHistory.ResetSession(currentSessionID, messages); err != nil {
					fmt.Fprintf(os.Stderr, "Could not load older display history: %v\n", err)
				}
				refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
			} else if currentSessionID != prevSessionID {
				if a.SyncSessionState != nil {
					if err := a.SyncSessionState(messages, allowedTools, currentSessionID, true); err != nil {
						fmt.Fprintf(ppWriter, "Could not save session state: %v\n", err)
					}
					theme = ui.GetConfiguredTheme(a.Config)
					kiReader.Theme = theme
				}
				chatHistory.SetSessionID(currentSessionID)
			}
			continue
		}

		kiReader.ActiveTurn.Store(true)
		collapseRedrawn := false
		// Consume redraw requests on the foreground renderer so committed
		// history and the current inference form one consistent frame.
		a.OnStreamProgress = func() bool {
			ui.GetUI().StateMu.Lock()
			inModal := ui.GetUI().InApprovalPrompt || ui.GetUI().InInteractiveMenu || terminal.Default().InAltScreen()
			ui.GetUI().StateMu.Unlock()
			if inModal {
				return false
			}
			collapseReplay := kiReader.ApplyPendingCollapse()
			if !collapseReplay {
				return false
			}
			chatHistory.RepaintTurns()
			collapseRedrawn = collapseRedrawn || collapseReplay
			return true
		}
		ppWriter.BeginScrollEntry()
		for _, previous := range messages {
			if previous.Role != "system" {
				ppWriter.EnsureActivityGap()
				break
			}
		}
		chatHistory.BeginTurn()
		renderTurnPromptHeader(ppWriter, a.Config, theme, promptPrefix, line, fromQueue, kiReader)
		executeREPLTurn(a, mam, ppWriter, &messages, line, allowedTools, theme, currentSessionID, fd, kiReader, attachmentSource, preExpanded, fromQueue)
		a.OnStreamProgress = nil
		kiReader.ActiveTurn.Store(false)
		chatHistory.FinishTurn(nil)
		// Both a deferred Ctrl+O and an unsafe live syntax repaint require
		// one authoritative replay, after tool calls and text reach history.
		redrawCollapse := kiReader.ApplyPendingCollapse()
		redrawSyntax := ppWriter.ConsumeSyntaxRefresh()
		if collapseRedrawn || redrawCollapse || redrawSyntax {
			chatHistory.RepaintTurns()
		}
		refreshREPLStatus(a, messages, allowedTools, mam, kiReader, rl, false)
	}

	if !a.Ephemeral {
		_ = db.SetLatestSessionIDContext(a.Context(), currentSessionID)
	}
}
