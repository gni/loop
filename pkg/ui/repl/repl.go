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

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	chathistory "loop/pkg/ui/chat/history"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

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

	ui.SetInteractive(true)
	defer ui.SetInteractive(false)

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
	terminal.Default().PushTitle(os.Stderr)
	ui.SetIdleTabTitle(os.Stderr, a, currentSessionID, "")
	ui.SetScrollRegionOffset(style.ConsoleScrollRegionOffset())
	ui.InitStatusBar(os.Stderr)
	a.RefreshPlanStatus()
	// Registered before history and resize cleanup so they stop drawing first.
	defer func() {
		cancel()
		ui.ShutdownStatusBar(os.Stderr)
		ui.TerminalMu.Lock()
		defer ui.TerminalMu.Unlock()
		if terminal.Default().InAltScreen() {
			ui.ForceExitAlternateScreen(os.Stderr)
			fmt.Fprint(os.Stderr, "\x1b[?7h\x1b[2J\x1b[H")
		} else {
			fmt.Fprint(os.Stderr, terminal.FullCleanupSequence+"\x1b[?7h\x1b[2J\x1b[H")
		}
		terminal.Default().RestoreTitle(os.Stderr)
		if !a.Ephemeral {
			fmt.Fprintf(os.Stderr, "goodbye! to resume this session, run: loop --session %s (or loop --resume)\r\n", render.SanitizeTerminalText(currentSessionID))
		}
	}()
	// Request modified Enter reporting where supported; legacy terminals ignore
	// these sequences and retain Ctrl+N/Alt+Enter/Ctrl+J as newline shortcuts.
	restoreKeys := terminal.Default().BeginEnhancedKeys(os.Stderr)
	defer restoreKeys()

	_, height := ui.GetTerminalSize()
	ppWriter := ui.NewPromptPreservingWriter(os.Stderr, height)
	ppWriter.SetNativeScroll(a.LiveSettings().NativeScroll)
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
		InputChan:    make(chan byte, 16384),
		ApprovalChan: make(chan byte, 4096),
		InjectChan:   make(chan byte, 256),
		Hist:         hist,
		HistoryIndex: -1,
	}
	configureREPLCallbacks(kiReader, &theme)
	ppWriter.SetFooterRedrawHook(func(dest io.Writer) {
		state := ui.GetUI()
		state.StateMu.Lock()
		state.LastStatusBarText = ""
		state.StateMu.Unlock()
		ui.DrawConsoleStaticControlsLocked(dest, a, kiReader, true)
	})
	chatHistory, historyErr := chathistory.New(a, kiReader, ppWriter, currentSessionID, messages)
	if historyErr != nil {
		fmt.Fprintf(os.Stderr, "Could not load older display history: %v\n", historyErr)
	}
	defer chatHistory.Close()
	kiReader.OnChatHistoryInput = chatHistory.FilterInput

	ui.GetUI().SetActiveInputReader(kiReader)
	defer ui.GetUI().SetActiveInputReader(nil)
	restorePaste := terminal.Default().BeginBracketedPaste(os.Stderr)
	defer restorePaste()

	startREPLSystemNotifications(a, kiReader)
	go startREPLRawInputDispatcher(startREPLInputReader(kiReader), kiReader)

	editor := NewPromptEditor(a, hist, kiReader, chatHistory)

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

	initialLive := a.LiveSettings()
	ui.SetCollapseStatus(initialLive.CollapseResults)
	initialStatusLimit := a.GetStatusContextLimit(initialPromptTokens)
	ui.UpdateStatus(initialLive.Model, initialPromptTokens, initialCompletionTokens, latestTurnTokens, initialStatusLimit, false, 0, activeTasks, initialLive.ShowTokens, initialTokensEstimated)
	ui.DrawStaticPromptSeparator(os.Stderr, initialLive.ShowThinking, initialLive.ReasoningEffort, theme)
	ui.GetUI().StateMu.Lock()
	savedStats := ui.GetUI().LastStatsText
	ui.GetUI().StateMu.Unlock()
	ui.DrawStaticStatsLine(os.Stderr, theme, "", savedStats)
	ui.DrawStatusBar(os.Stderr, theme)
	ui.SetIdleTabTitle(os.Stderr, a, currentSessionID, mam.ActiveAgentName())

	defer watchREPLResizes(a, kiReader)()

	for {
		if ctx.Err() != nil {
			break
		}
		ui.SetIdleTabTitle(os.Stderr, a, currentSessionID, mam.ActiveAgentName())
		promptPrefix := ui.GetPromptSymbol(nil)
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
			kiReader.MergePastedCodeBlocks(queued.CodeBlocks)
			fromQueue = true
			kiReader.NotifyQueueChanged()
		} else {
			kiReader.ResetEditorState()

			uncommitted, pastes, blocks := kiReader.TakeTypeAheadWithBlocks()
			// A draft already belongs to the editor, not to paste detection.
			// Feed it before live keys without blocking on the injection channel
			// or turning a following Enter into part of a multiline paste.
			kiReader.SetPromptDraft("", 0)
			if len(uncommitted) > 0 {
				kiReader.PastedLiterals = pastes
				kiReader.MergePastedCodeBlocks(blocks)
				kiReader.PasteRemaining = append(kiReader.QueuePromptPaste(string(uncommitted)), kiReader.PasteRemaining...)
			}

			ppWriter.SetPromptCol(1 + style.CellWidth(promptPrefix))
			ui.TerminalMu.Lock()
			ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, true)
			ui.TerminalMu.Unlock()

			ppWriter.SetCursorHidden(false)

			oldState, errRaw := term.MakeRaw(fd)
			if errRaw != nil {
				fmt.Printf("Error setting terminal raw mode: %v\n", errRaw)
				os.Exit(1)
			}

			kiReader.IsAtMainPrompt.Store(true)
			line, err = editor.ReadLine()
			kiReader.IsAtMainPrompt.Store(false)
			kiReader.SetPromptDraft("", 0)
			chatHistory.CloseSlashMenu()
			term.Restore(fd, oldState)
			if ctx.Err() != nil {
				break
			}
		}

		// Sync local theme in case the user cycled themes via ◐ click or Ctrl+Y while editing.
		liveAfterPrompt := a.LiveSettings()
		theme = style.ResolveConfiguredTheme(liveAfterPrompt.Theme, liveAfterPrompt.SyntaxTheme)
		if fromQueue {
			ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader)
		} else {
			_ = ui.ResetPromptAfterSubmit(os.Stderr, a, ppWriter, theme, promptPrefix)
		}

		if !fromQueue {
			if pasted := kiReader.TakePastedText(); pasted != "" {
				line += pasted
				ui.GetUI().SetPasteLinesOffset(0)
				ui.InitStatusBar(os.Stderr)
			}

			if kiReader.CtrlCInterrupted {
				ui.GetUI().ClearPromptHint()
				kiReader.CtrlCInterrupted = false
				kiReader.SetPromptDraft("", 0)
				kiReader.SetPastedText("")
				_ = kiReader.TakePastedCodeBlocks()
				kiReader.ResetTypeAhead()
				kiReader.ClearQueue()
				ui.GetUI().StateMu.Lock()
				ui.GetUI().PasteLinesOffset = 0
				ui.GetUI().State.QueuedPromptsCount = 0
				ui.GetUI().StateMu.Unlock()
				kiReader.Drain()
				refreshREPLStatus(a, messages, allowedTools, mam, kiReader, true)
				continue
			}

			if err != nil {
				isEOF := err == io.EOF || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF")
				if isEOF && term.IsTerminal(fd) {
					if ui.GetUI().CheckCtrlDConfirmation(3 * time.Second) {
						break
					}
					ui.GetUI().SetPromptHint("(Press Ctrl+D again to exit)", 3*time.Second, func() {
						ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader)
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
		if blocks := kiReader.TakePastedCodeBlocks(); len(blocks) > 0 {
			line = interceptor.ExpandPromptCodeBlocks(line, blocks)
		}

		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(commandLine, "/system_event ") {
			continue
		}

		if !strings.HasPrefix(line, "/") {
			hist.Add(line)
		}

		directCommands := false
		a.LLMProviderMu.RLock()
		if a.Config != nil {
			directCommands = a.Config.DirectCommands
		}
		a.LLMProviderMu.RUnlock()
		if isCmd, _ := parseManualCommand(commandLine, directCommands); isCmd {
			_, cmdStr := parseManualCommand(line, directCommands)
			ui.TerminalMu.Lock()
			ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, false)
			ui.TerminalMu.Unlock()
			shell.execute(cmdStr, line, promptPrefix, a, ppWriter, &messages, currentSessionID, kiReader, theme)
			refreshREPLStatus(a, messages, allowedTools, mam, kiReader, false)
			continue
		}

		preExpanded, handled, quit := false, false, false
		prevSessionID := currentSessionID
		_ = ui.ConsumeSessionReloaded()
		if attachmentSource == nil || strings.HasPrefix(strings.TrimSpace(commandLine), "/") || commands.IsHelpCommand(commandLine) || commands.IsBuiltinSlashCommand(trimmedLine) {
			line, preExpanded, handled, quit = dispatchREPLSlashCommand(a, line, &messages, allowedTools, &theme, ppWriter, &currentSessionID, hist, mam, kiReader)
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
					live := a.LiveSettings()
					theme = style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
					kiReader.SetTheme(theme)
					allowedTools = slices.Clone(a.ToolSelection)
					kiReader.SetAllowedTools(allowedTools)
				}
				if err := chatHistory.ResetSession(currentSessionID, messages); err != nil {
					fmt.Fprintf(os.Stderr, "Could not load older display history: %v\n", err)
				}
				ui.ResetTabTitleTopic()
				refreshREPLStatus(a, messages, allowedTools, mam, kiReader, false)
				ui.SetIdleTabTitle(os.Stderr, a, currentSessionID, mam.ActiveAgentName())
			} else if currentSessionID != prevSessionID {
				if a.SyncSessionState != nil {
					if err := a.SyncSessionState(messages, allowedTools, currentSessionID, true); err != nil {
						fmt.Fprintf(ppWriter, "Could not save session state: %v\n", err)
					}
					live := a.LiveSettings()
					theme = style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
					kiReader.SetTheme(theme)
				}
				chatHistory.SetSessionID(currentSessionID)
				ui.ResetTabTitleTopic()
				ui.SetIdleTabTitle(os.Stderr, a, currentSessionID, mam.ActiveAgentName())
			}
			continue
		}

		kiReader.ActiveTurn.Store(true)
		turnRedrawn := false
		// Consume redraw requests on the foreground renderer so committed
		// history and the current inference form one consistent frame.
		a.OnStreamProgress = func() bool {
			if ui.GetUI().IsInModal() || a.LiveSettings().NativeScroll {
				return false
			}
			collapseReplay := kiReader.ApplyPendingCollapse()
			themeReplay := kiReader.ConsumePendingThemeRefresh()
			if collapseReplay || themeReplay {
				chatHistory.RepaintTurns()
			} else if !chatHistory.RepaintPendingTurn() {
				return false
			}
			turnRedrawn = true
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
		renderTurnPromptHeader(ppWriter, a, theme, promptPrefix, line, fromQueue, kiReader)
		executeREPLTurn(a, mam, ppWriter, &messages, line, allowedTools, theme, currentSessionID, fd, kiReader, attachmentSource, preExpanded, fromQueue)
		turnRedrawn = chatHistory.RepaintPendingTurn() || turnRedrawn
		a.OnStreamProgress = nil
		chatHistory.FinishTurn(nil)
		// Both a deferred Ctrl+O, mid-turn theme switch, and an unsafe live
		// syntax repaint require one authoritative replay after messages settle.
		redrawCollapse := kiReader.ApplyPendingCollapse()
		redrawTheme := kiReader.ConsumePendingThemeRefresh()
		redrawSyntax := ppWriter.ConsumeSyntaxRefresh()
		if redrawTheme {
			liveAfter := a.LiveSettings()
			theme = style.ResolveConfiguredTheme(liveAfter.Theme, liveAfter.SyntaxTheme)
		}
		if a.LiveSettings().NativeScroll {
			if redrawCollapse || redrawTheme {
				ui.RedrawScreen(os.Stderr, a, kiReader)
			}
		} else if turnRedrawn || redrawCollapse || redrawTheme || redrawSyntax {
			chatHistory.RepaintTurns()
		}
		refreshREPLStatus(a, messages, allowedTools, mam, kiReader, false)
		kiReader.ActiveTurn.Store(false)
	}

	if !a.Ephemeral {
		_ = db.SetLatestSessionIDContext(a.Context(), currentSessionID)
	}
}
