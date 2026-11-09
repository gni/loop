package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
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
	searchScan                                      *historySearchScan
	searchQuery, searchNotice                       string
	searchDraft                                     string
	searchEditing                                   bool
	initialLoading, initialOutput                   bool
	initialFilling                                  bool
	initialRows                                     int
	searchRow, searchDirection                      int
	searchPending                                   bool
	previousSink                                    func(agent.Event) error
	logoEnabled, logoVisible                        bool
	logoFrame                                       int
	logoStop, logoDone                              chan struct{}
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
	pendingOlderTurn                                *displayTurn
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
	resetSequence                                   atomic.Uint64
	closed                                          atomic.Bool
}

type displayTurn struct {
	// Fragment rows remain unchanged until the whole turn is loaded.
	fragment                   *displayTurn
	fragmentHead, fragmentTail bool
	start, end                 int
	messages                   []db.Message
	offset                     int64
	size                       int
	appearance                 string
	alternate                  *cachedTurnLayout
	failedLayout               string
}

// PrintInitialHistory schedules the newest display block without blocking input.
// Earlier blocks remain on disk until prefetch or navigation needs them.
func (h *ChatHistory) PrintInitialHistory() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	h.printInitialHistoryLocked()
}

func (h *ChatHistory) printInitialHistoryLocked() {
	if h.closed.Load() {
		return
	}
	h.historyStart = h.buffer.Base() + h.writer.printLine - 1
	if h.writer.pendingLineFeed {
		h.historyStart++
	}
	h.captureOnly = false
	h.initialLoading = h.hasOlderLocked()
	h.initialOutput = false
	h.initialRows = 0
	h.prefetchOlderLocked()
	h.paintLocked()
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
	width := 0
	if c.MarkdownRender {
		width = h.buffer.Width
	}
	return fmt.Sprintf("%s/%s/%t/%t/%t/%s/%t/%d", c.Theme, c.SyntaxTheme, c.MarkdownRender, c.ShowThinking, c.CollapseResults, c.ReasoningEffort, c.ShowMicroseconds, width)
}

// BeginTurn/FinishTurn retain semantic boundaries only for explicit appearance
// changes. Scrolling, streaming and ordinary redraws use actual display rows.
func (h *ChatHistory) BeginTurn() {
	TerminalMu.Lock()
	h.searchQuery, h.searchNotice = "", ""
	h.searchEditing = false
	h.searchDraft = ""
	h.searchPending = false
	h.searchScan = nil
	h.searchRow = -1
	defer TerminalMu.Unlock()
	if h.browsing {
		h.top = h.buffer.Base()
		h.browsing = false
		h.pendingUp = 0
		h.pendingHome = false
		h.layoutScroll = 0
		h.paintLocked()
	}
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
	state := getUI()
	state.StateMu.Lock()
	pasteRows := state.PasteLinesOffset
	state.StateMu.Unlock()
	bottom := style.ConsoleScrollBottom(height, pasteRows)
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
	if h.searchScan != nil {
		anchors = append(anchors, &h.searchScan.next)
	}
	if h.searchQuery != "" && h.searchRow >= 0 {
		anchors = append(anchors, &h.searchRow)
	}
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
	rows = h.decorateChatLocked(rows)
	rows = h.searchRowsLocked(rows)
	hint := ""
	if h.browsing {
		total := h.buffer.Base() + h.buffer.Height
		text := fmt.Sprintf("History · lines %d-%d of %d loaded", h.top+1, min(h.top+len(rows), total), total)
		if h.hasOlderLocked() {
			text += " · more above"
		}
		text += " · [Back to latest: End] · Shift+drag: select"
		hint = style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Border).Render(style.TruncateCells(text, max(1, width-1)))
	}
	if h.searchNotice != "" {
		notice := h.searchNotice
		if strings.HasPrefix(notice, "Find: ") && h.searchRow >= 0 {
			notice = h.searchMatchNoticeLocked()
		}
		hint = style.TruncateCells(notice, max(1, width-1))
	}
	if h.initialLoading {
		hint = "Loading conversation…"
	}
	if h.searchPending {
		hint = style.TruncateCells("Searching conversation… · Esc to cancel", max(1, width-1))
	} else if h.pageJob != nil && (h.pendingUp > 0 || h.pendingHome) {
		hint = style.TruncateCells("Loading older messages… · End: latest", max(1, width-1))
	}
	if h.searchEditing {
		hint = style.TruncateCells("Search: "+h.searchDraft+" | Enter: find | Esc: cancel", max(1, width-1))
	}
	if err := errors.Join(h.historyErr, h.buffer.Err()); err != nil {
		hint = style.TruncateCells("history: "+err.Error(), max(1, width-1))
	}
	render.DrawChatViewport(h.reader.W, rows, h.drawn, hint, h.drawnHint)
	h.drawn = rows
	h.drawnHint = hint
	if hint == "" {
		state := getUI()
		state.StateMu.Lock()
		stats := state.LastStatsText
		state.StateMu.Unlock()
		DrawStaticStatsLineLocked(h.reader.W, GetConfiguredTheme(h.a.Config), "", stats)
	}
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
			if visible && turn.fragment == nil && turn.appearance != key && !attempted[turn] {
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
		h.adjustSearchRowsLocked(start, end, turn.end, delta, padding)
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
