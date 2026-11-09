package ui

import (
	"fmt"
	"time"

	"loop/pkg/ui/style"
)

// Find searches rendered chat, loading earlier disk-backed pages only as needed.
func (h *ChatHistory) Find(query string, direction int) error {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.closed.Load() {
		return fmt.Errorf("history is closed")
	}
	h.searchPending = false
	h.searchScan = nil
	h.initialFilling = false
	h.pendingUp = 0
	h.pendingHome = false
	h.layoutScroll = 0
	if query != "" {
		h.searchQuery = query
		h.searchRow = h.buffer.Base() + h.buffer.Height
		direction = -1
	}
	if h.searchQuery == "" {
		return fmt.Errorf("use /find <text> first")
	}
	if direction == 0 {
		direction = 1
	}
	h.searchDirection = direction
	h.searchNotice = ""
	h.searchPending = true
	h.scanSearchLocked(h.searchRow+direction, direction, func() {
		if h.hasOlderLocked() {
			h.continueSearchLocked()
		} else {
			h.wrapSearchLocked()
		}
	})
	return nil
}

func (h *ChatHistory) Latest() {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	if h.closed.Load() {
		return
	}
	h.searchQuery, h.searchNotice = "", ""
	h.searchEditing = false
	h.searchDraft = ""
	h.searchPending = false
	h.searchScan = nil
	h.searchRow = -1
	h.pendingUp = 0
	h.pendingHome = false
	h.layoutScroll = 0
	h.top = h.buffer.Base()
	h.browsing = false
	h.paintLocked()
}

func (h *ChatHistory) showSearchRowLocked(row int) {
	h.searchRow = row
	h.searchPending = false
	h.searchScan = nil
	// Keep nearby matches in place so moving between similar lines visibly
	// moves the highlight instead of recentering identical-looking text.
	if row < h.top || row >= h.top+h.buffer.Height {
		h.top = min(h.buffer.Base(), max(0, row-h.buffer.Height/2))
	}
	h.browsing = h.top < h.buffer.Base()
	h.searchNotice = h.searchMatchNoticeLocked()
	h.paintLocked()
}

func (h *ChatHistory) wrapSearchLocked() {
	from := 0
	if h.searchDirection < 0 {
		from = h.buffer.Base() + h.buffer.Height - 1
	}
	h.scanSearchLocked(from, h.searchDirection, func() {
		h.searchPending = false
		h.searchScan = nil
		h.searchNotice = "No matches: " + h.searchQuery
		h.paintLocked()
	})
}

func (h *ChatHistory) continueSearchLocked() {
	if !h.searchPending || h.searchScan != nil {
		return
	}
	if h.readyPage != nil {
		added := h.applyOlderPageLocked()
		if added > 0 && h.searchDirection < 0 {
			h.scanSearchLocked(h.historyStart+added-1, -1, h.continueSearchLocked)
			return
		}
	}
	if !h.hasOlderLocked() {
		h.wrapSearchLocked()
		return
	}
	h.prefetchOlderLocked()
	h.paintLocked()
}

// Scan a bounded number of loaded rows per turn of the event loop. The
// terminal lock is released between batches so typing and Escape stay usable.
type historySearchScan struct {
	next, direction int
	exhausted       func()
}

func (h *ChatHistory) scanSearchLocked(start, direction int, exhausted func()) {
	scan := &historySearchScan{next: start, direction: direction, exhausted: exhausted}
	h.searchScan = scan
	h.searchPending = true
	h.stepSearchLocked(scan)
	if h.searchScan == scan {
		h.paintLocked()
	}
}

func (h *ChatHistory) stepSearchLocked(scan *historySearchScan) {
	if h.closed.Load() || h.searchScan != scan || !h.searchPending {
		return
	}
	count := h.buffer.HistoryRowCount()
	if scan.direction < 0 {
		scan.next = min(scan.next, count-1)
	} else {
		scan.next = max(0, scan.next)
	}
	if scan.next < 0 || scan.next >= count {
		h.searchScan = nil
		scan.exhausted()
		return
	}
	const batch = 512
	if row := h.buffer.FindRow(h.searchQuery, scan.next, scan.direction, batch); row >= 0 {
		h.searchScan = nil
		h.showSearchRowLocked(row)
		return
	}
	scan.next += scan.direction * batch
	if scan.next < 0 || scan.next >= count {
		h.searchScan = nil
		scan.exhausted()
		return
	}
	time.AfterFunc(time.Millisecond, func() {
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		h.stepSearchLocked(scan)
	})
}

func (h *ChatHistory) adjustSearchRowsLocked(start, end, newEnd, delta, padding int) {
	anchors := []*int{&h.searchRow}
	if h.searchScan != nil {
		anchors = append(anchors, &h.searchScan.next)
	}
	for _, row := range anchors {
		if *row < 0 {
			continue
		}
		if *row >= end {
			*row += delta + padding
		} else if *row >= start {
			*row = start + padding + min(*row-start, max(0, newEnd-start-1))
		}
	}
}

func (h *ChatHistory) searchRowsLocked(rows []string) []string {
	if h.searchQuery != "" && h.searchRow >= h.top && h.searchRow < h.top+len(rows) {
		index := h.searchRow - h.top
		rows[index] = style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Highlight).Underline(true).Render(style.StripAnsi(rows[index]))
	}
	return rows
}

func (h *ChatHistory) searchMatchNoticeLocked() string {
	return fmt.Sprintf("Find: %s · line %d · Ctrl+N/P: next/prev · End: latest", h.searchQuery, h.searchRow+1)
}
