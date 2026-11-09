package ui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func (h *ChatHistory) FilterInput(key []byte) []byte {
	if len(key) == 0 || h.closed.Load() {
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
	if handled := h.filterSearchInput(key); handled {
		return nil
	}
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	h.resizeLocked(GetTerminalSize())
	wasSearching := h.searchPending
	h.searchPending = false
	h.searchScan = nil
	if wasSearching {
		h.searchNotice = "Search stopped"
		h.needsPaint = true
		h.queuePaintLocked()
	}
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
		if h.browsing || h.searchQuery != "" {
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
					if button&0xc3 == 0 && h.browsing {
						if y, e := strconv.Atoi(fields[2]); e == nil && y == h.buffer.Height+style.ActivityGapLines+1 {
							action = "latest"
						}
					}
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
	if mouse && delta == 0 && action != "latest" {
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
		h.searchQuery, h.searchNotice = "", ""
		h.searchEditing = false
		h.searchDraft = ""
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
	// Start the refresh even when a stale turn holds the viewport in place.
	if h.layoutScroll != 0 {
		h.scheduleLayoutsLocked()
	}
	if (h.pendingUp > 0 || h.pendingHome) && h.pageJob != nil {
		h.needsPaint = true
		h.queuePaintLocked()
	}
	if h.top != oldTop || action == "latest" {
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

// Search input stays out of the prompt so an unfinished message survives.
func (h *ChatHistory) filterSearchInput(key []byte) bool {
	if !h.reader.IsAtMainPrompt.Load() {
		return false
	}
	TerminalMu.Lock()
	action := string(key)
	if action == "\x06" {
		h.searchEditing = true
		h.searchDraft = h.searchQuery
		h.searchPending = false
		h.searchScan = nil
		h.paintLocked()
		TerminalMu.Unlock()
		return true
	}
	if !h.searchEditing {
		query := h.searchQuery
		TerminalMu.Unlock()
		if query != "" && (action == "\x0e" || action == "\x10") {
			direction := 1
			if action == "\x10" {
				direction = -1
			}
			_ = h.Find("", direction)
			return true
		}
		return false
	}
	switch action {
	case "\r", "\n":
		query := h.searchDraft
		h.searchEditing = false
		h.paintLocked()
		TerminalMu.Unlock()
		if query != "" {
			_ = h.Find(query, 0)
		} else {
			TerminalMu.Lock()
			h.searchQuery, h.searchNotice = "", ""
			h.searchRow = -1
			h.searchPending = false
			h.searchScan = nil
			h.paintLocked()
			TerminalMu.Unlock()
		}
		return true
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~", "\x1b[1;5F":
		TerminalMu.Unlock()
		h.Latest()
		return true
	case "\x1b", "\x03":
		h.searchEditing = false
	case "\x7f", "\x08":
		if h.searchDraft != "" {
			_, size := utf8.DecodeLastRuneInString(h.searchDraft)
			h.searchDraft = h.searchDraft[:len(h.searchDraft)-size]
		}
	case "\x15":
		h.searchDraft = ""
	default:
		if strings.HasPrefix(action, "\x1b[200~") && strings.HasSuffix(action, "\x1b[201~") {
			action = strings.TrimSuffix(strings.TrimPrefix(action, "\x1b[200~"), "\x1b[201~")
			action = strings.Join(strings.Fields(action), " ")
		}
		if !strings.HasPrefix(action, "\x1b") {
			for _, r := range action {
				if r >= 32 && r != 127 {
					h.searchDraft += string(r)
				}
			}
		}
	}
	h.paintLocked()
	TerminalMu.Unlock()
	return true
}
