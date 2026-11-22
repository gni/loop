package ui

import (
	"errors"
	"io"
	"os"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/terminal"
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

func NewChatHistory(a *agent.Agent, reader *interceptor.KeyInterceptorReader, writer *PromptPreservingWriter, id string, initial []db.Message) (*ChatHistory, error) {
	width, height := GetTerminalSize()
	pasteOffset := getUI().GetPasteLinesOffset()
	h := &ChatHistory{a: a, reader: reader, writer: writer, captureOnly: true,
		buffer: render.NewScrollback(width, style.ConsoleScrollBottom(height, pasteOffset))}
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
	var err error
	h.source, err = h.openHistory(id, initial)
	err = errors.Join(err, pageErr, turnErr)
	h.sessionID = id
	h.previousSink = a.EventSink
	a.EventSink = func(event agent.Event) error {
		if event.Type == "message_end" && event.Message != nil {
			TerminalMu.Lock()
			if !h.closed.Load() && h.active != nil && (event.SessionID == "" || event.SessionID == h.sessionID) && event.Message.Role != "system" {
				h.active.messages = append(h.active.messages, message.Clone(*event.Message))
			}
			TerminalMu.Unlock()
		}
		if h.previousSink != nil {
			return h.previousSink(event)
		}
		return nil
	}
	h.appearance = h.appearanceKey()
	TerminalMu.Lock()
	writer.inner = &historyOutput{destination: writer.inner, history: h}
	getUI().SetChatHistory(h)
	if reader != nil && reader.W != nil {
		h.stopMouse = terminal.Default().BeginMouseTracking(reader.W)
	}
	TerminalMu.Unlock()
	h.setSessionContext(id)
	h.startChatLogo()
	return h, err
}

func (h *ChatHistory) ResetSession(id string, initial []db.Message) error {
	if h.closed.Load() {
		return io.ErrClosedPipe
	}
	sequence := h.resetSequence.Add(1)
	source, err := h.openHistory(id, initial)
	if err != nil {
		if source != nil {
			_ = source.Close()
		}
		return err
	}
	width, height := GetTerminalSize()
	buffer, pageErr := render.NewPagedScrollback(width, style.ConsoleScrollBottom(height, getUI().GetPasteLinesOffset()))
	if pageErr != nil {
		if source != nil {
			_ = source.Close()
		}
		return errors.Join(err, pageErr)
	}
	if pageSize := configuredHistoryPageSize(h.a); pageSize > 0 {
		buffer.SetPageSize(pageSize)
	}
	turnFile, turnErr := os.CreateTemp("", "loop-display-turns-*")
	if turnErr != nil {
		_ = buffer.Close()
		if source != nil {
			_ = source.Close()
		}
		return errors.Join(err, turnErr)
	}
	_ = os.Remove(turnFile.Name())
	TerminalMu.Lock()
	if h.closed.Load() || h.resetSequence.Load() != sequence {
		TerminalMu.Unlock()
		_ = buffer.Close()
		_ = turnFile.Close()
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
	h.drawn, h.drawnHint = nil, ""
	h.logoFrame = 0
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
	if notice != "" {
		live := h.a.LiveSettings()
		noticeStyle := style.NewStyle().Foreground(style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme).Success).Italic(true)
		_, _ = h.writer.writeLocked([]byte(noticeStyle.Render(notice) + "\n"))
	}
	h.setSessionContext(id)
	h.printInitialHistoryLocked()
	TerminalMu.Unlock()
	return nil
}

func (h *ChatHistory) SetNotice(notice string) {
	TerminalMu.Lock()
	h.notice = notice
	TerminalMu.Unlock()
}

func (h *ChatHistory) SetSessionID(id string) {
	TerminalMu.Lock()
	h.sessionID = id
	TerminalMu.Unlock()
}

func (h *ChatHistory) Close() {
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	if h.logoStop != nil {
		close(h.logoStop)
		<-h.logoDone
	}
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.stopMouse != nil {
		h.stopMouse()
		h.stopMouse = nil
	}
	h.closePagesLocked()
	if h.a != nil {
		h.a.EventSink = h.previousSink
	}
	if h.writer != nil {
		if output, ok := h.writer.inner.(*historyOutput); ok && output.history == h {
			h.writer.inner = output.destination
		}
	}
	getUI().ClearChatHistoryIf(h)
}
