package history

import (
	"context"
	"errors"
	"io"
	"os"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func configuredHistoryPageSize(a *agent.Agent) int {
	if a == nil {
		return 0
	}
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if a.Config == nil {
		return 0
	}
	return a.Config.HistoryPageSize
}

// New initializes the chat history viewport, paged scrollback buffer, mouse
// tracking, and startup logo animation for the active session.
func New(a *agent.Agent, reader *interceptor.KeyInterceptorReader, writer *ui.PromptPreservingWriter, id string, initial []db.Message) (*ChatHistory, error) {
	width, height := ui.GetTerminalSize()
	pasteOffset := ui.GetUI().GetPasteLinesOffset()
	nativeScroll := a != nil && a.LiveSettings().NativeScroll
	h := &ChatHistory{a: a, reader: reader, writer: writer, captureOnly: true,
		buffer: render.NewScrollback(width, style.ConsoleScrollBottom(height, pasteOffset))}
	var err error
	if !nativeScroll {
		paged, pageErr := render.NewPagedScrollback(width, h.buffer.Height)
		if pageErr == nil {
			if pageSize := configuredHistoryPageSize(a); pageSize > 0 {
				paged.SetPageSize(pageSize)
			}
			h.buffer = paged
		}
		turnFile, turnErr := os.CreateTemp("", "loop-display-turns-*")
		if turnErr == nil && turnFile != nil {
			_ = os.Remove(turnFile.Name())
		}
		h.turnFile = turnFile
		h.source, err = h.openHistory(id, initial)
		err = errors.Join(err, pageErr, turnErr)
	}
	h.sessionID = id
	h.previousSink = a.EventSink
	a.EventSink = func(event agent.Event) error {
		if event.Type == "message_end" && event.Message != nil {
			ui.TerminalMu.Lock()
			if !h.closed.Load() && h.active != nil && (event.SessionID == "" || event.SessionID == h.sessionID) && event.Message.Role != "system" {
				h.active.messages = append(h.active.messages, message.Clone(*event.Message))
			}
			ui.TerminalMu.Unlock()
		}
		if h.previousSink != nil {
			return h.previousSink(event)
		}
		return nil
	}
	h.appearance = h.appearanceKey()
	ui.TerminalMu.Lock()
	writer.SetInnerLocked(&historyOutput{destination: writer.InnerLocked(), history: h})
	writer.SetNativeOutputHooksLocked(h.onNativeBeforeWriteLocked, h.onNativeOutputLocked)
	ui.GetUI().SetChatHistory(h)
	if reader != nil && reader.W != nil {
		if a != nil {
			live := a.LiveSettings()
			terminal.Default().SetMouseEnabled(!live.NativeScroll)
			terminal.Default().SetMouseMotion(live.HoverTooltips)
		}
		h.stopMouse = terminal.Default().BeginMouseTracking(reader.W)
	}
	ui.TerminalMu.Unlock()
	h.setSessionContext(id)
	h.startChatLogo()
	return h, err
}

func (h *ChatHistory) ResetSession(id string, initial []db.Message) error {
	if h.closed.Load() {
		return io.ErrClosedPipe
	}
	sequence := h.resetSequence.Add(1)
	width, height := ui.GetTerminalSize()
	nativeScroll := h.isNativeScroll()
	var (
		source   displayHistorySource
		buffer   *render.Scrollback
		turnFile *os.File
		err      error
	)
	if nativeScroll {
		buffer = render.NewScrollback(width, style.ConsoleScrollBottom(height, ui.GetUI().GetPasteLinesOffset()))
	} else {
		source, err = h.openHistory(id, initial)
		if err != nil {
			if source != nil {
				_ = source.Close()
			}
			return err
		}
		var pageErr error
		buffer, pageErr = render.NewPagedScrollback(width, style.ConsoleScrollBottom(height, ui.GetUI().GetPasteLinesOffset()))
		if pageErr != nil {
			if source != nil {
				_ = source.Close()
			}
			return errors.Join(err, pageErr)
		}
		if pageSize := configuredHistoryPageSize(h.a); pageSize > 0 {
			buffer.SetPageSize(pageSize)
		}
		var turnErr error
		turnFile, turnErr = os.CreateTemp("", "loop-display-turns-*")
		if turnErr != nil {
			_ = buffer.Close()
			if source != nil {
				_ = source.Close()
			}
			return errors.Join(err, turnErr)
		}
		_ = os.Remove(turnFile.Name())
	}
	ui.TerminalMu.Lock()
	if h.closed.Load() || h.resetSequence.Load() != sequence {
		ui.TerminalMu.Unlock()
		if buffer != nil {
			_ = buffer.Close()
		}
		if turnFile != nil {
			_ = turnFile.Close()
		}
		if source != nil {
			_ = source.Close()
		}
		return io.ErrClosedPipe
	}
	h.closePagesLocked()
	h.searchQuery, h.searchNotice = "", ""
	h.setSearchEditingLocked(false)
	h.searchDraft = ""
	h.searchPending = false
	h.searchScan = nil
	h.searchRow = -1
	h.slashForced = false
	h.slashDismissedQuery = ""
	h.slashItems = nil
	h.slashQuery = ""
	h.slashTitle = ""
	h.slashSelected = 0
	h.slashScroll = 0
	h.setSlashMenuOpenLocked(false)
	h.drawn, h.drawnHint = nil, ""
	h.logoFrame = 0
	h.logoVisible = false
	h.nativeHasOutput = false
	h.buffer = buffer
	h.source, h.historyErr = source, nil
	h.turnMu.Lock()
	h.turnFile, h.turnOffset = turnFile, 0
	h.turnMu.Unlock()
	h.top, h.historyStart = 0, 0
	h.browsing = false
	h.captureOnly = true
	h.turns = nil
	h.active = nil
	h.writer.ResetAfterRedrawLocked(height)
	h.sessionID = id
	h.appearance = h.appearanceKey()
	notice := h.notice
	h.notice = ""
	if h.isNativeScroll() {
		h.captureOnly = false
		h.initialLoading = false
		h.initialFilling = false
		h.setSessionContext(id)
		ui.TerminalMu.Unlock()
		if h.reader != nil && h.reader.W != nil {
			ui.RedrawScreenWithNotice(h.reader.W, h.a, h.reader, notice)
		}
		return nil
	}
	if notice != "" {
		live := h.a.LiveSettings()
		noticeStyle := style.NewStyle().Foreground(style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme).Success).Italic(true)
		_, _ = h.writer.WriteLocked([]byte(noticeStyle.Render(notice) + "\n"))
	}
	h.setSessionContext(id)
	h.printInitialHistoryLocked()
	ui.TerminalMu.Unlock()
	return nil
}

func (h *ChatHistory) SetNotice(notice string) {
	ui.TerminalMu.Lock()
	h.notice = notice
	ui.TerminalMu.Unlock()
}

func (h *ChatHistory) SetSessionID(id string) {
	ui.TerminalMu.Lock()
	h.sessionID = id
	ui.TerminalMu.Unlock()
}

func (h *ChatHistory) Close() {
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	if h.logoStop != nil {
		close(h.logoStop)
		<-h.logoDone
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.stopMouse != nil {
		h.stopMouse()
		h.stopMouse = nil
	}
	h.closePagesLocked()
	if h.a != nil {
		h.a.EventSink = h.previousSink
	}
	if h.writer != nil {
		h.writer.SetNativeOutputHooksLocked(nil, nil)
		if output, ok := h.writer.InnerLocked().(*historyOutput); ok && output.history == h {
			h.writer.SetInnerLocked(output.destination)
		}
	}
	ui.GetUI().ClearChatHistoryIf(h)
}

// Resolve display metadata once per session switch, not on every status redraw.
func (h *ChatHistory) setSessionContext(id string) {
	label, known := ui.SessionDisplayLabel(h.a, id)

	state := ui.GetUI()
	state.StateMu.Lock()
	state.SessionID = id
	state.State.SessionLabel = label
	state.StateMu.Unlock()
	if known || h.a.Ephemeral {
		return
	}
	store := h.a.Store
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	go func() {
		var sessions []db.SessionInfo
		if contextual, ok := store.(interface {
			GetSessionsContext(context.Context) ([]db.SessionInfo, error)
		}); ok {
			sessions, _ = contextual.GetSessionsContext(h.a.Context())
		} else if store != nil {
			sessions, _ = store.GetSessions()
		} else {
			sessions, _ = db.GetSessionsContext(h.a.Context())
		}
		for _, session := range sessions {
			if session.SessionID != id || session.Name == "" {
				continue
			}
			ui.TerminalMu.Lock()
			defer ui.TerminalMu.Unlock()
			if h.closed.Load() {
				return
			}
			state.StateMu.Lock()
			if state.SessionID != id || state.State.SessionLabel != label {
				state.StateMu.Unlock()
				return
			}
			state.State.SessionLabel = session.Name
			state.StateMu.Unlock()
			ui.DrawStatusBarLocked(h.reader.W, theme)
			ui.RefreshTabTitleLocked(h.reader.W, h.a)
			return
		}
	}()
}
