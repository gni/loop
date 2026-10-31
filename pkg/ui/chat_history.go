package ui

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// ChatHistory owns the rendered chat document. TerminalMu protects the buffer,
// viewport and writer positions. Navigation never reloads or formats messages.
type ChatHistory struct {
	previousSink                                    func(agent.Event) error
	sessionID                                       string
	drawn                                           []string
	drawnHint                                       string
	a                                               *agent.Agent
	reader                                          *interceptor.KeyInterceptorReader
	writer                                          *PromptPreservingWriter
	buffer                                          *render.Scrollback
	top                                             int
	browsing, paintPending, captureOnly, needsPaint bool
	initial                                         []db.Message
	turns                                           []*displayTurn
	active                                          *displayTurn
	appearance                                      string
	notice                                          string
	closed                                          atomic.Bool
}

type displayTurn struct {
	start, end int
	messages   []db.Message
}

func NewChatHistory(a *agent.Agent, reader *interceptor.KeyInterceptorReader, writer *PromptPreservingWriter, id string, initial []db.Message) (*ChatHistory, error) {
	width, height := GetTerminalSize()
	h := &ChatHistory{a: a, reader: reader, writer: writer, captureOnly: true,
		buffer: render.NewScrollback(width, style.ConsoleScrollBottom(height, getUI().PasteLinesOffset))}
	var err error
	h.initial, err = h.loadMessages(id, initial)
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
	writer.inner = &historyOutput{destination: writer.inner, history: h}
	getUI().ChatHistory = h
	fmt.Fprint(reader.W, "\x1b[?1000h\x1b[?1006h")
	return h, err
}

func (h *ChatHistory) loadMessages(id string, initial []db.Message) ([]db.Message, error) {
	messages := message.CloneAll(initial)
	if h.a.Ephemeral || id == "" {
		return messages, nil
	}
	store, ok := h.a.Store.(interface {
		Entries(string) ([]session.Record, error)
	})
	if !ok {
		if defaultStore, err := db.DefaultStore(); err == nil {
			store = defaultStore
		}
	}
	if store == nil {
		return messages, nil
	}
	entries, err := store.Entries(id)
	if err != nil {
		return messages, err
	}
	var full []db.Message
	for _, record := range session.LeafPath(entries, "") {
		switch record.Type {
		case "", "message":
			if record.Message.Role != "" {
				full = append(full, record.Message)
			}
		case "context":
			if len(full) == 0 {
				full = append(full, record.Messages...)
			}
		}
	}
	if len(full) > 0 {
		messages = full
	}
	return messages, nil
}

// PrintInitialHistory renders saved messages once, including pre-compaction
// records. Subsequent scrolls use their captured display rows.
func (h *ChatHistory) PrintInitialHistory() {
	var transcript bytes.Buffer
	PrintSessionHistory(&transcript, h.initial, GetConfiguredTheme(h.a.Config), h.a.Config)
	if transcript.Len() > 0 {
		h.BeginTurn()
		_, _ = h.writer.Write(transcript.Bytes())
		h.FinishTurn(h.initial)
	}
	h.initial = nil
	TerminalMu.Lock()
	h.captureOnly = false
	h.paintLocked()
	TerminalMu.Unlock()
}

func (h *ChatHistory) ResetSession(id string, initial []db.Message) error {
	messages, err := h.loadMessages(id, initial)
	TerminalMu.Lock()
	width, height := GetTerminalSize()
	h.drawn = nil
	h.buffer = render.NewScrollback(width, style.ConsoleScrollBottom(height, getUI().PasteLinesOffset))
	h.top = 0
	h.browsing = false
	h.captureOnly = true
	h.turns = nil
	h.active = nil
	h.writer.ResetAfterRedrawLocked(height)
	h.initial = messages
	h.sessionID = id
	notice := h.notice
	h.notice = ""
	TerminalMu.Unlock()
	PrintBanner(h.writer, h.a)
	if notice != "" {
		fmt.Fprintln(h.writer, notice)
	}
	h.PrintInitialHistory()
	return err
}

func (h *ChatHistory) appearanceKey() string {
	c := h.a.Config
	return fmt.Sprintf("%s/%s/%t/%t/%t/%s/%t", c.Theme, c.SyntaxTheme, c.MarkdownRender, c.ShowThinking, c.CollapseResults, c.ReasoningEffort, c.ShowMicroseconds)
}

// BeginTurn/FinishTurn retain semantic boundaries only for explicit appearance
// changes. Scrolling, streaming and ordinary redraws use actual display rows.
func (h *ChatHistory) BeginTurn() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	start := h.buffer.Base() + h.writer.printLine - 1
	if h.writer.pendingLineFeed {
		start++
	}
	turn := &displayTurn{start: start, end: start}
	h.turns = append(h.turns, turn)
	h.active = turn
}

func (h *ChatHistory) FinishTurn(messages []db.Message) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.active != nil {
		if messages != nil {
			h.active.messages = message.CloneAll(messages)
		}
		h.active.end = h.buffer.Base() + h.writer.printLine
		h.active = nil
	}
}

func (h *ChatHistory) resizeLocked(width, height int) {
	bottom := style.ConsoleScrollBottom(height, getUI().PasteLinesOffset)
	if width == h.buffer.Width && bottom == h.buffer.Height {
		return
	}
	var anchors []*int
	for _, turn := range h.turns {
		anchors = append(anchors, &turn.start, &turn.end)
	}
	col := h.writer.printCol
	if h.writer.autoWrapPending {
		col++
	}
	h.writer.printLine, col, h.top = h.buffer.Resize(width, bottom, h.writer.printLine, col, h.top, anchors...)
	h.writer.printCol = min(width, col)
	h.writer.autoWrapPending = col > width
	h.writer.autoWrapDetached = h.writer.autoWrapPending
	h.drawn = nil
	h.needsPaint = true
}

func (h *ChatHistory) paintLocked() {
	width, height := GetTerminalSize()
	h.resizeLocked(width, height)
	if !h.browsing {
		h.top = h.buffer.Base()
	}
	h.top = min(max(0, h.top), h.buffer.Base())
	h.browsing = h.browsing && h.top < h.buffer.Base()
	rows := h.buffer.View(h.top)
	hint := ""
	if h.browsing {
		total := h.buffer.Base() + h.buffer.Height
		text := fmt.Sprintf("history %d–%d / %d · End: latest · Shift+drag: select", h.top+1, min(h.top+len(rows), total), total)
		hint = style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Border).Render(style.TruncateRunes(text, max(1, width-1)))
	}
	render.DrawChatViewport(h.reader.W, rows, h.drawn, hint, h.drawnHint)
	h.drawn = rows
	h.drawnHint = hint
	h.needsPaint = false
}

func (h *ChatHistory) queuePaintLocked() {
	if h.paintPending || h.captureOnly {
		return
	}
	h.paintPending = true
	time.AfterFunc(16*time.Millisecond, func() {
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		h.paintPending = false
		getUI().StateMu.Lock()
		modal := getUI().InApprovalPrompt
		getUI().StateMu.Unlock()
		if !h.closed.Load() && !modal && (h.browsing || h.needsPaint) {
			h.paintLocked()
		}
	})
}

// RepaintTurns is used by explicit collapse/expand and final syntax refresh.
// Replace only agent blocks, preserving command listings at their positions.
func (h *ChatHistory) RepaintTurns() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	h.repaintTurnsLocked()
	h.paintLocked()
}

func (h *ChatHistory) repaintTurnsLocked() {
	h.resizeLocked(GetTerminalSize())
	theme := GetConfiguredTheme(h.a.Config)
	var replay func(io.Writer, bool, style.UITheme)
	if h.active != nil {
		h.a.CurrentStreamMu.Lock()
		replay = h.a.CurrentStreamReplay
		h.a.CurrentStreamMu.Unlock()
	}

	for i, turn := range h.turns {
		messages := turn.messages
		if len(messages) == 0 {
			continue
		}
		var out bytes.Buffer
		PrintSessionHistory(&out, messages, theme, h.a.Config)
		if turn == h.active && replay != nil {
			if messages[len(messages)-1].Role == "user" {
				fmt.Fprintln(&out, style.ActivityDivider(theme))
			}
			replay(&out, h.a.Config.CollapseResults, theme)
		}
		start, end := turn.start, turn.end
		delta, padding := h.buffer.ReplaceRange(start, end, strings.TrimRight(out.String(), "\r\n"))
		turn.start += padding
		turn.end = end + delta + padding
		for _, earlier := range h.turns[:i] {
			earlier.start += padding
			earlier.end += padding
		}
		for _, later := range h.turns[i+1:] {
			later.start += delta + padding
			later.end += delta + padding
		}
		if h.top >= end {
			h.top += delta + padding
		} else if h.top >= start {
			h.top = start + padding
		} else {
			h.top += padding
		}
	}
	// The stream continues at the end of its block. Local output after a finished
	// block stays in place and remains the writer's current tail.
	transcript := []byte(strings.Join(h.buffer.View(h.buffer.Base()), "\r\n"))
	h.writer.ResetAfterReplayLocked(h.writer.height, transcript)
	if counter, ok := h.a.CurrentWriter().(interface{ ResetAfterReplay([]byte) }); ok {
		counter.ResetAfterReplay(transcript)
	}
	h.appearance = h.appearanceKey()
}

// RedrawLocked repaints the existing document rather than reconstructing it.
func (h *ChatHistory) RedrawLocked() bool {
	if h.closed.Load() {
		return false
	}
	if h.appearance != h.appearanceKey() {
		h.repaintTurnsLocked()
	}
	h.drawn = nil
	h.paintLocked()
	return true
}

func (h *ChatHistory) FilterInput(key []byte) []byte {
	if h.closed.Load() {
		return key
	}
	action := string(key)
	mouse := strings.HasPrefix(action, "\x1b[<") || strings.HasPrefix(action, "\x1b[M")
	getUI().StateMu.Lock()
	modal := getUI().InApprovalPrompt
	getUI().StateMu.Unlock()
	if modal || (!h.reader.ActiveTurn.Load() && !h.reader.IsAtMainPrompt.Load()) {
		if mouse {
			return nil
		}
		return key
	}
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	h.resizeLocked(GetTerminalSize())
	delta := 0
	navigation := true
	switch action {
	case "\x1b[5~":
		delta = -max(1, h.buffer.Height-1)
	case "\x1b[6~":
		delta = max(1, h.buffer.Height-1)
	case "\x1b[1;5H":
		action = "top"
	case "\x1b[1;5F":
		action = "latest"
	case "\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~":
		if h.browsing {
			action = "top"
		} else {
			navigation = false
		}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~", "\x1b":
		if h.browsing {
			action = "latest"
		} else {
			navigation = false
		}
	default:
		navigation = mouse
		if strings.HasPrefix(action, "\x1b[<") && strings.HasSuffix(action, "M") {
			fields := strings.Split(action[3:len(action)-1], ";")
			if len(fields) == 3 {
				button, err := strconv.Atoi(fields[0])
				if err == nil {
					switch button & 0xc3 {
					case 64:
						delta = -3
					case 65:
						delta = 3
					}
				}
			}
		} else if len(key) == 6 && strings.HasPrefix(action, "\x1b[M") && key[3] >= 32 {
			switch (key[3] - 32) & 0xc3 {
			case 64:
				delta = -3
			case 65:
				delta = 3
			}
		}
	}
	if mouse && delta == 0 {
		return nil
	}
	if !h.browsing {
		h.top = h.buffer.Base()
	}
	oldTop := h.top
	switch {
	case action == "latest":
		h.top = h.buffer.Base()
	case action == "top":
		h.top = 0
	case delta != 0:
		h.top = min(h.buffer.Base(), max(0, h.top+delta))
	case !navigation:
		h.top = h.buffer.Base()
	}
	h.browsing = h.top < h.buffer.Base()
	if h.top != oldTop {
		if !h.browsing {
			h.paintLocked()
		} else {
			h.queuePaintLocked()
		}
	}
	if navigation {
		return nil
	}
	return key
}

func (h *ChatHistory) Close() {
	h.closed.Store(true)
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	fmt.Fprint(h.reader.W, "\x1b[?1000l\x1b[?1006l")
	h.a.EventSink = h.previousSink
	if output, ok := h.writer.inner.(*historyOutput); ok && output.history == h {
		h.writer.inner = output.destination
	}
	if getUI().ChatHistory == h {
		getUI().ChatHistory = nil
	}
}

type historyOutput struct {
	destination io.Writer
	history     *ChatHistory
}

func (w *historyOutput) Write(data []byte) (int, error) {
	h := w.history
	dropped := h.buffer.Dropped()
	h.buffer.Write(data)
	if delta := h.buffer.Dropped() - dropped; delta > 0 {
		h.top = max(0, h.top-delta)
		for _, turn := range h.turns {
			turn.start = max(0, turn.start-delta)
			turn.end = max(0, turn.end-delta)
		}
	}
	if h.active != nil {
		h.active.end = h.buffer.Base() + h.writer.printLine
	}
	if h.captureOnly {
		return len(data), nil
	}
	if h.browsing {
		h.queuePaintLocked()
		return len(data), nil
	}
	h.drawn = nil
	return w.destination.Write(data)
}
