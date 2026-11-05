package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/terminal"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// ChatHistory owns the rendered chat document. TerminalMu protects the buffer,
// viewport and writer positions. Older session turns and rendered row pages
// are loaded only as needed.
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
	source                                          displayHistorySource
	pageJob                                         *historyPageJob
	readyPage                                       *preparedHistoryPage
	pendingOlderRows                                *render.Scrollback
	pendingUp                                       int
	pendingHome                                     bool
	scrollDirection                                 int
	layoutJob                                       *turnLayoutJob
	layoutScroll                                    int
	turnMu                                          sync.Mutex
	historyStart                                    int
	turnFile                                        *os.File
	turnOffset                                      int64
	historyErr                                      error
	turns                                           []*displayTurn
	active                                          *displayTurn
	appearance                                      string
	notice                                          string
	closed                                          atomic.Bool
}

type displayTurn struct {
	start, end   int
	messages     []db.Message
	offset       int64
	size         int
	appearance   string
	alternate    *cachedTurnLayout
	failedLayout string
}

func NewChatHistory(a *agent.Agent, reader *interceptor.KeyInterceptorReader, writer *PromptPreservingWriter, id string, initial []db.Message) (*ChatHistory, error) {
	width, height := GetTerminalSize()
	h := &ChatHistory{a: a, reader: reader, writer: writer, captureOnly: true,
		buffer: render.NewScrollback(width, style.ConsoleScrollBottom(height, getUI().PasteLinesOffset))}
	paged, pageErr := render.NewPagedScrollback(width, h.buffer.Height)
	if pageErr == nil {
		if a != nil && a.Config != nil && a.Config.HistoryPageSize > 0 {
			paged.SetPageSize(a.Config.HistoryPageSize)
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
	writer.inner = &historyOutput{destination: writer.inner, history: h}
	getUI().ChatHistory = h
	fmt.Fprint(reader.W, "\x1b[?1000h\x1b[?1006h")
	return h, err
}

// PrintInitialHistory loads the newest display page. Earlier turns stay on disk
// until navigation reaches the beginning of the loaded document.
func (h *ChatHistory) PrintInitialHistory() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	h.historyStart = h.buffer.Base() + h.writer.printLine - 1
	if h.writer.pendingLineFeed {
		h.historyStart++
	}
	added := h.loadOlderLocked()
	if added > 0 {
		end := h.historyStart + added
		dropped := h.buffer.FinishRestore(end)
		h.historyStart -= dropped
		h.top = max(0, h.top-dropped)
		for _, turn := range h.turns {
			turn.start -= dropped
			turn.end -= dropped
		}
		end -= dropped
		transcript := []byte(strings.Join(h.buffer.View(h.buffer.Base()), "\r\n"))
		if end <= h.buffer.Height {
			transcript = []byte(strings.Join(h.buffer.View(0)[:end], "\r\n"))
		}
		h.writer.ResetAfterReplayLocked(h.writer.height, transcript)
	}
	h.captureOnly = false
	h.paintLocked()
	h.prefetchOlderLocked()
}

func (h *ChatHistory) ResetSession(id string, initial []db.Message) error {
	source, err := h.openHistory(id, initial)
	width, height := GetTerminalSize()
	buffer, pageErr := render.NewPagedScrollback(width, style.ConsoleScrollBottom(height, getUI().PasteLinesOffset))
	if pageErr != nil {
		if source != nil {
			_ = source.Close()
		}
		return errors.Join(err, pageErr)
	}
	if h.a != nil && h.a.Config != nil && h.a.Config.HistoryPageSize > 0 {
		buffer.SetPageSize(h.a.Config.HistoryPageSize)
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
	h.closePagesLocked()
	h.drawn, h.drawnHint = nil, ""
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
	TerminalMu.Unlock()
	PrintBanner(h.writer, h.a)
	if notice != "" {
		noticeStyle := style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Success).Italic(true)
		fmt.Fprintln(h.writer, noticeStyle.Render(notice))
	}
	h.PrintInitialHistory()
	return err
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

func (h *ChatHistory) SetPageSize(size int) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.buffer != nil && size > 0 {
		h.buffer.SetPageSize(size)
	}
}

func (h *ChatHistory) TotalLoadedLines() int {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.buffer != nil {
		return h.buffer.Base() + h.buffer.Height
	}
	return 0
}

func (h *ChatHistory) AppearanceKey() string {
	return h.appearanceKey()
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
	turn := &displayTurn{start: start, end: start, appearance: h.appearanceKey()}
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
		h.active.appearance = h.appearanceKey()
		if err := h.archiveTurn(h.active); err != nil {
			h.historyErr = err
		}
		h.active = nil
	}
}

func (h *ChatHistory) resizeLocked(width, height int) {
	bottom := style.ConsoleScrollBottom(height, getUI().PasteLinesOffset)
	if width == h.buffer.Width && bottom == h.buffer.Height {
		return
	}
	if width != h.buffer.Width {
		for _, turn := range h.turns {
			if turn.alternate != nil {
				_ = turn.alternate.rows.Close()
				turn.alternate = nil
			}
		}
	}
	anchors := []*int{&h.historyStart}
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
	h.repaintTurnsLocked()
	h.refreshStalePageLocked()
	if !h.browsing {
		h.top = h.buffer.Base()
	}
	h.top = min(max(0, h.top), h.buffer.Base())
	h.browsing = h.browsing && h.top < h.buffer.Base()
	rows := h.buffer.View(h.top)
	hint := ""
	if h.browsing {
		total := h.buffer.Base() + h.buffer.Height
		text := fmt.Sprintf("History · lines %d–%d of %d loaded", h.top+1, min(h.top+len(rows), total), total)
		if h.hasOlderLocked() {
			text += " · more above"
		}
		text += " · End: latest · Shift+drag: select"
		hint = style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Border).Render(style.TruncateRunes(text, max(1, width-1)))
	}
	if err := errors.Join(h.historyErr, h.buffer.Err()); err != nil {
		hint = style.TruncateRunes("history: "+err.Error(), max(1, width-1))
	}
	render.DrawChatViewport(h.reader.W, rows, h.drawn, hint, h.drawnHint)
	h.drawn = rows
	h.drawnHint = hint
	h.needsPaint = false
	h.scheduleLayoutsLocked()
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
		if h.closed.Load() {
			return
		}
		getUI().StateMu.Lock()
		modal := getUI().InApprovalPrompt || getUI().InInteractiveMenu || terminal.Default().InAltScreen()
		getUI().StateMu.Unlock()
		if !modal && (h.browsing || h.needsPaint) {
			h.paintLocked()
		}
	})
}

// RepaintTurns is used by explicit collapse/expand and final syntax refresh.
// Replace only agent blocks, preserving command listings at their positions.
func (h *ChatHistory) RepaintTurns() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if len(h.turns) > 0 {
		h.turns[len(h.turns)-1].appearance = ""
		h.turns[len(h.turns)-1].failedLayout = ""
	}
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

	changed := false
	replayedActive := false
	key := h.appearanceKey()
	attempted := make(map[*displayTurn]bool)
	for {
		viewTop := h.top
		if !h.browsing {
			viewTop = h.buffer.Base()
		}
		i := -1
		for candidate, turn := range h.turns {
			visible := turn == h.active || (turn.end > viewTop && turn.start < viewTop+h.buffer.Height)
			if visible && turn.appearance != key && !attempted[turn] {
				i = candidate
				break
			}
		}
		if i < 0 {
			break
		}
		turn := h.turns[i]
		attempted[turn] = true
		if turn != h.active {
			if cached := turn.alternate; cached != nil && cached.key == key && cached.rows.Width == h.buffer.Width {
				h.restoreTurnLayoutLocked(i, cached.rows, key)
				changed = true
				continue
			}
		}
		replayedActive = true
		messages, err := h.turnMessages(turn)
		if err != nil {
			h.historyErr = fmt.Errorf("load display turn: %w", err)
			continue
		}
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
		changed = true
		start, end := turn.start, turn.end
		delta, padding := h.buffer.ReplaceRange(start, end, strings.TrimRight(out.String(), "\r\n"))
		turn.start += padding
		turn.end = end + delta + padding
		turn.appearance = key
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
			if h.browsing && h.scrollDirection < 0 && delta > 0 && h.top > start {
				// Entering from below must reveal the tail of the expanded block.
				h.top = turn.end - (end - h.top)
			} else {
				h.top = start + padding + min(h.top-start, max(0, turn.end-turn.start-h.buffer.Height))
			}
		} else {
			h.top += padding
		}
	}
	if !changed {
		h.appearance = h.appearanceKey()
		return
	}
	if h.active != nil && !replayedActive {
		h.appearance = key
		return
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
	modal := getUI().InApprovalPrompt || getUI().InInteractiveMenu || terminal.Default().InAltScreen()
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
	if delta < 0 {
		h.scrollDirection = -1
	} else if delta > 0 {
		h.scrollDirection = 1
	} else if action == "top" || action == "latest" || !navigation {
		h.scrollDirection = 0
	}
	if (delta > 0 && h.layoutScroll < 0) || (delta < 0 && h.layoutScroll > 0) {
		h.layoutScroll = 0
	}
	oldTop := h.top
	advanced := false
	if delta > 0 || action == "latest" || !navigation {
		h.pendingUp = 0
		h.pendingHome = false
	}
	if action == "top" || delta < 0 {
		if h.readyPage != nil && (action == "top" || h.top+delta <= h.historyStart+h.prefetchMarginLocked()) {
			added := h.applyOlderPageLocked()
			oldTop += added
			advanced = added > 0
		}
		if h.hasOlderLocked() && (action == "top" || h.top+delta <= h.historyStart+h.prefetchMarginLocked()) {
			h.prefetchOlderLocked()
		}
	}
	minimum := 0
	if h.hasOlderLocked() {
		minimum = h.historyStart
	}
	switch {
	case action == "latest":
		h.top = h.buffer.Base()
	case action == "top":
		h.top = minimum
		h.pendingUp = 0
		h.pendingHome = h.hasOlderLocked() && !advanced
	case delta != 0:
		if delta < 0 && h.hasOlderLocked() {
			h.pendingUp += max(0, minimum-(h.top+delta))
		}
		h.top = min(h.buffer.Base(), max(minimum, h.top+delta))
	case !navigation:
		h.top = h.buffer.Base()
	}
	if delta != 0 {
		h.top = h.limitLayoutScrollLocked(oldTop, h.top)
	} else if action == "top" || action == "latest" || !navigation {
		h.layoutScroll = 0
	}
	if delta != 0 {
		h.buffer.Prefetch(h.top, delta)
	} else if action == "top" {
		h.buffer.Prefetch(h.top, -1)
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
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	fmt.Fprint(h.reader.W, "\x1b[?1000l\x1b[?1006l")
	h.closePagesLocked()
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
	if err := h.buffer.Err(); err != nil {
		h.historyErr = err
	}
	if delta := h.buffer.Dropped() - dropped; delta > 0 {
		h.top = max(0, h.top-delta)
		h.historyStart = max(0, h.historyStart-delta)
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
