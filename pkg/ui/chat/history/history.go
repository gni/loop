package history

import (
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
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// ChatHistory owns the rendered chat document. ui.TerminalMu protects the buffer,
// viewport and writer positions. Older session turns and rendered row pages
// are loaded only as needed.
type ChatHistory struct {
	searchScan                                      *historySearchScan
	searchQuery, searchNotice                       string
	searchDraft                                     string
	searchEditing                                   bool
	searchEditingAtomic                             atomic.Bool
	slashMenuOpen                                   bool
	slashMenuOpenAtomic                             atomic.Bool
	slashForced                                     bool
	slashQuery, slashTitle, slashDismissedQuery     string
	slashItems                                      []render.SlashCommandItem
	slashSelected, slashScroll                      int
	slashMenuHeaderRow                              int
	slashMenuStartRow, slashMenuEndRow              int
	slashVisibleStart                               int
	slashExtraCache                                 []render.SlashCommandItem
	slashExtraFetched                               time.Time
	slashExtraLoaded                                bool
	initialLoading, initialOutput                   bool
	initialFilling                                  bool
	initialRows                                     int
	searchRow, searchDirection                      int
	searchPending                                   bool
	previousSink                                    func(agent.Event) error
	stopMouse                                       func()
	logoEnabled, logoVisible                        bool
	logoFrame                                       int
	logoStop, logoDone                              chan struct{}
	sessionID                                       string
	drawn                                           []string
	drawnHint                                       string
	a                                               *agent.Agent
	reader                                          *interceptor.KeyInterceptorReader
	writer                                          *ui.PromptPreservingWriter
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
	replayPending, allowActiveReplay                bool
	notice                                          string
	sel                                             chatSelection
	selActiveAtomic                                 atomic.Bool
	resetSequence                                   atomic.Uint64
	closed                                          atomic.Bool
}

func (h *ChatHistory) setSearchEditingLocked(editing bool) {
	h.searchEditing = editing
	h.searchEditingAtomic.Store(editing)
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

func (h *ChatHistory) isNativeScroll() bool {
	if h == nil || h.a == nil {
		return false
	}
	return h.a.LiveSettings().NativeScroll
}

// HasOlder reports whether unloaded older session turns still remain on disk.
func (h *ChatHistory) HasOlder() bool {
	if h == nil || h.closed.Load() {
		return false
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	return h.hasOlderLocked()
}

// PrintInitialHistory schedules the newest display block without blocking input.
// Earlier blocks remain on disk until prefetch or navigation needs them.
func (h *ChatHistory) PrintInitialHistory() {
	if h.isNativeScroll() {
		ui.TerminalMu.Lock()
		h.captureOnly = false
		h.initialLoading = false
		h.initialFilling = false
		ui.TerminalMu.Unlock()
		if h.reader != nil && h.reader.W != nil {
			ui.RedrawScreen(h.reader.W, h.a, h.reader)
		}
		return
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	h.printInitialHistoryLocked()
}

func (h *ChatHistory) printInitialHistoryLocked() {
	if h.closed.Load() {
		return
	}
	h.historyStart = h.buffer.Base() + h.writer.PrintLineLocked() - 1
	if h.writer.PendingLineFeedLocked() {
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
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.buffer != nil && size > 0 {
		h.buffer.SetPageSize(size)
	}
}

func (h *ChatHistory) TotalLoadedLines() int {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.buffer != nil {
		return h.buffer.Base() + h.buffer.Height
	}
	return 0
}

func (h *ChatHistory) AppearanceKey() string {
	return h.appearanceKey()
}

func (h *ChatHistory) appearanceKey() string {
	live := h.a.LiveSettings()
	width := 0
	if live.MarkdownRender && h.buffer != nil {
		width = h.buffer.Width
	}
	return fmt.Sprintf("%s/%s/%t/%t/%t/%s/%t/%d", live.Theme, live.SyntaxTheme, live.MarkdownRender, live.ShowThinking, live.CollapseResults, live.ReasoningEffort, live.ShowMicroseconds, width)
}

// BeginTurn/FinishTurn retain semantic boundaries only for explicit appearance
// changes. Scrolling, streaming and ordinary redraws use actual display rows.
func (h *ChatHistory) BeginTurn() {
	ui.TerminalMu.Lock()
	h.replayPending = false
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
	h.clearSelectionLocked()
	defer ui.TerminalMu.Unlock()
	if h.browsing {
		h.top = h.buffer.Base()
		h.browsing = false
		h.pendingUp = 0
		h.pendingHome = false
		h.layoutScroll = 0
		h.paintLocked()
	}
	start := h.buffer.Base() + h.writer.PrintLineLocked() - 1
	if h.writer.PendingLineFeedLocked() {
		start++
	}
	turn := &displayTurn{start: start, end: start, appearance: h.appearanceKey()}
	h.turns = append(h.turns, turn)
	h.active = turn
}

func (h *ChatHistory) FinishTurn(messages []db.Message) {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.active != nil {
		if messages != nil {
			h.active.messages = message.CloneAll(messages)
		}
		h.active.end = h.buffer.Base() + h.writer.PrintLineLocked()
		h.active.appearance = h.appearanceKey()
		if err := h.archiveTurn(h.active); err != nil {
			h.historyErr = err
		}
		h.active = nil
	}
}

func (h *ChatHistory) resizeLocked(width, height int) {
	pasteRows := ui.GetUI().GetPasteLinesOffset()
	bottom := style.ConsoleScrollBottom(height, pasteRows)
	if width == h.buffer.Width && bottom == h.buffer.Height {
		return
	}
	if width != h.buffer.Width {
		h.clearSelectionLocked()
		for _, turn := range h.turns {
			if turn.alternate != nil {
				_ = turn.alternate.rows.Close()
				turn.alternate = nil
			}
		}
	}
	anchors := []*int{&h.historyStart}
	if h.sel.active || h.sel.hasRange {
		anchors = append(anchors, &h.sel.anchorRow, &h.sel.focusRow)
	}
	if h.searchScan != nil {
		anchors = append(anchors, &h.searchScan.next)
	}
	if h.searchQuery != "" && h.searchRow >= 0 {
		anchors = append(anchors, &h.searchRow)
	}
	for _, turn := range h.turns {
		anchors = append(anchors, &turn.start, &turn.end)
	}
	col := h.writer.PrintColLocked()
	if h.writer.AutoWrapPendingLocked() {
		col++
	}
	newLine, col, newTop := h.buffer.Resize(width, bottom, h.writer.PrintLineLocked(), col, h.top, anchors...)
	h.top = newTop
	h.writer.SetCursorStateLocked(newLine, min(width, col), col > width)
	h.drawn = nil
	h.needsPaint = true
}

func (h *ChatHistory) paintLocked() {
	if h.isNativeScroll() {
		h.needsPaint = false
		return
	}
	width, height := ui.GetTerminalSize()
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
	rows = h.selectionRowsLocked(rows)
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	hint := ""
	if h.browsing {
		firstRow := h.buffer.FirstContentRow()
		total := max(1, h.buffer.Base()+h.buffer.Height-firstRow)
		startLine := max(1, h.top-firstRow+1)
		endLine := min(total, startLine+len(rows)-1)
		text := fmt.Sprintf("History · lines %d-%d of %d loaded", startLine, endLine, total)
		if h.hasOlderLocked() {
			text += " · more above"
		}
		ui.GetUI().StateMu.Lock()
		approval := ui.GetUI().InApprovalPrompt
		ui.GetUI().StateMu.Unlock()
		if approval {
			text += " · Approval pending · End: choices"
		} else {
			text += " · [Back to latest: End] · Drag: select & copy"
		}
		hint = style.NewStyle().Foreground(theme.TextMuted).Render(style.TruncateCells(text, max(1, width-1)))
	}
	if h.sel.active && h.sel.hasRange {
		linesSel := h.selectionLineCountLocked()
		text := fmt.Sprintf("Selecting %d lines · drag edge or scroll wheel · release to copy", linesSel)
		if linesSel == 1 {
			text = "Selecting 1 line · drag edge or scroll wheel · release to copy"
		}
		hint = style.NewStyle().Foreground(theme.Primary).Render(style.TruncateCells(text, max(1, width-1)))
	} else if h.sel.copiedNotice != "" {
		hint = style.NewStyle().Foreground(theme.Success).Render(style.TruncateCells(h.sel.copiedNotice, max(1, width-1)))
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
	if hint == "" && h.shouldRenderSlashMenuLocked() {
		hint = h.slashMenuHintLocked(width, theme)
	}
	if err := errors.Join(h.historyErr, h.buffer.Err()); err != nil {
		hint = style.TruncateCells("history: "+err.Error(), max(1, width-1))
	}
	fallbackStats := ""
	if hint == "" {
		state := ui.GetUI()
		state.StateMu.Lock()
		stats := state.LastStatsText
		if state.ActivityStatsText != "" {
			stats = state.ActivityStatsText
		}
		state.StateMu.Unlock()
		fallbackStats = render.FormatStaticStatsContent("", stats, theme)
	}
	render.DrawChatViewportAtomic(h.reader.W, rows, h.drawn, hint, h.drawnHint, fallbackStats)
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
		ui.TerminalMu.Lock()
		defer ui.TerminalMu.Unlock()
		h.paintPending = false
		if h.closed.Load() {
			return
		}
		ui.GetUI().StateMu.Lock()
		modal := ui.GetUI().InInteractiveMenu || terminal.Default().InAltScreen()
		ui.GetUI().StateMu.Unlock()
		if !modal && (h.browsing || h.needsPaint) {
			h.paintLocked()
		}
	})
}

// RepaintTurns runs on the foreground renderer during an active turn. Replace
// only agent blocks, preserving command listings at their positions.
func (h *ChatHistory) RepaintTurns() {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	h.repaintActiveTurnLocked()
}

// RepaintPendingTurn lets the foreground stream consume a resize or appearance
// change without taking a renderer lock from the terminal input/resize thread.
func (h *ChatHistory) RepaintPendingTurn() bool {
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if h.closed.Load() || !h.replayPending {
		return false
	}
	h.repaintActiveTurnLocked()
	return true
}

func (h *ChatHistory) repaintActiveTurnLocked() {
	h.replayPending = false
	h.allowActiveReplay = true
	defer func() { h.allowActiveReplay = false }()
	if len(h.turns) > 0 {
		h.turns[len(h.turns)-1].appearance = ""
		h.turns[len(h.turns)-1].failedLayout = ""
	}
	h.paintLocked()
}

func (h *ChatHistory) repaintTurnsLocked() {
	h.resizeLocked(ui.GetTerminalSize())
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	cfgSnapshot, cfgErr := h.a.Configuration()
	if cfgErr != nil && h.a != nil {
		cfgSnapshot = h.a.Config
	}
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
				if turn == h.active && h.reader.ActiveTurn.Load() && !h.allowActiveReplay {
					h.replayPending = true
					continue
				}
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
		rows := h.buffer.NewHistoryPage()
		out := &turnReplayWriter{historyPageWriter: historyPageWriter{rows: rows}}
		ui.PrintSessionHistory(out, messages, theme, cfgSnapshot)
		if turn == h.active && replay != nil {
			if messages[len(messages)-1].Role == "user" {
				fmt.Fprintln(out, style.ActivityDivider(theme))
			}
			replay(out, live.CollapseResults, theme)
		}
		if err := errors.Join(rows.Err(), out.err); err != nil {
			h.historyErr = fmt.Errorf("rebuild display turn: %w", err)
			_ = rows.Close()
			continue
		}
		changed = true
		start, end := turn.start, turn.end
		old, delta, padding := h.buffer.SwapHistoryRange(start, end, rows)
		_ = old.Close()
		_ = rows.Close()
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
		h.adjustSelectionRowsLocked(start, end, turn.end, delta, padding)
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
	h.writer.ResetAfterReplayLocked(h.writer.Height(), transcript)
	if counter, ok := h.a.CurrentWriter().(interface{ ResetAfterReplay([]byte) }); ok {
		counter.ResetAfterReplay(transcript)
	}
	h.appearance = h.appearanceKey()
}

// RedrawLocked repaints the existing document rather than reconstructing it.
func (h *ChatHistory) RedrawLocked() bool {
	if h.closed.Load() || h.isNativeScroll() {
		return false
	}
	if h.appearance != h.appearanceKey() {
		h.repaintTurnsLocked()
	}
	h.drawn = nil
	h.drawnHint = ""
	h.paintLocked()
	return true
}

// ResizeLocked resizes the scrollback viewport (caller holds ui.TerminalMu).
func (h *ChatHistory) ResizeLocked(width, height int) {
	if h.closed.Load() {
		return
	}
	h.resizeLocked(width, height)
}

// PaintIfNeededLocked repaints the viewport if dirty and active (caller holds ui.TerminalMu).
func (h *ChatHistory) PaintIfNeededLocked() {
	if h.closed.Load() || h.captureOnly || !h.needsPaint {
		return
	}
	h.paintLocked()
}

// DrawnHintLocked returns the active viewport hint text (caller holds ui.TerminalMu).
func (h *ChatHistory) DrawnHintLocked() string {
	if h.closed.Load() {
		return ""
	}
	return h.drawnHint
}

// SetNoticeLocked sets a pending notice while ui.TerminalMu is held.
func (h *ChatHistory) SetNoticeLocked(notice string) {
	h.notice = notice
}

// Writer returns the prompt-preserving writer attached to this history.
func (h *ChatHistory) Writer() *ui.PromptPreservingWriter {
	return h.writer
}
