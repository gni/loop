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
	// Fast-path Ctrl+C outside search editing without acquiring TerminalMu so
	// cancellation is never stalled behind heavy disk or Chroma rendering.
	if len(key) == 1 && key[0] == 3 && !h.searchEditingAtomic.Load() {
		return key
	}
	action := string(key)
	mouse := strings.HasPrefix(action, "\x1b[<") || strings.HasPrefix(action, "\x1b[M")
	getUI().StateMu.Lock()
	approval := getUI().InApprovalPrompt
	modal := getUI().InInteractiveMenu || terminal.Default().InAltScreen()
	getUI().StateMu.Unlock()
	if modal || (!approval && !h.reader.ActiveTurn.Load() && !h.reader.IsAtMainPrompt.Load()) {
		if mouse {
			return nil
		}
		return key
	}
	// Approval arrows and shortcuts belong to the choice menu. Bring it back
	// into view before forwarding input; scroll keys must never select a choice.
	if approval && !mouse && !approvalHistoryNavigation(action) {
		TerminalMu.Lock()
		browsing := h.browsing
		TerminalMu.Unlock()
		if browsing {
			h.Latest()
		}
		return key
	}
	if !approval && h.filterSearchInput(key) {
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
		if h.browsing || approval {
			action = "top"
		} else {
			navigation = false
		}
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~", "\x1b":
		if h.browsing || h.searchQuery != "" || approval {
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
						x, _ := strconv.Atoi(fields[1])
						if y, e := strconv.Atoi(fields[2]); e == nil {
							if y == h.buffer.Height+style.ActivityGapLines+1 {
								action = "latest"
							} else if y == 1 && h.a != nil && h.a.LiveSettings().ShowStickyQuestion {
								if x >= max(1, h.buffer.Width-4) {
									action = "next_question"
								} else {
									action = "prev_question"
								}
							}
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
			btn := (key[3] - 32) & 0xc3
			if btn == 0 && h.browsing {
				clickX := int(key[4]) - 32
				clickY := int(key[5]) - 32
				if clickY == h.buffer.Height+style.ActivityGapLines+1 {
					action = "latest"
				} else if clickY == 1 && h.a != nil && h.a.LiveSettings().ShowStickyQuestion {
					if clickX >= max(1, h.buffer.Width-4) {
						action = "next_question"
					} else {
						action = "prev_question"
					}
				}
			}
			switch btn {
			case 64:
				delta = -3
			case 65:
				delta = 3
			}
		}
	}
	if mouse && delta == 0 && action != "latest" && action != "prev_question" && action != "next_question" {
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
	minimum := h.buffer.FirstContentRow()
	if h.hasOlderLocked() {
		minimum = max(minimum, h.historyStart)
	}
	switch {
	case action == "latest":
		h.searchQuery, h.searchNotice = "", ""
		h.setSearchEditingLocked(false)
		h.searchDraft = ""
		h.top = h.buffer.Base()
	case action == "prev_question":
		h.stepQuestionLocked(-1, &oldTop)
	case action == "next_question":
		h.stepQuestionLocked(1, &oldTop)
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
	} else if action == "top" || action == "latest" || action == "prev_question" || action == "next_question" || !navigation {
		h.layoutScroll = 0
	}
	if delta != 0 {
		h.buffer.Prefetch(h.top, delta)
	} else if action == "top" || action == "prev_question" {
		h.buffer.Prefetch(h.top, -1)
	} else if action == "next_question" {
		h.buffer.Prefetch(h.top, 1)
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
	if h.top != oldTop || action == "latest" || action == "prev_question" || action == "next_question" {
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

func (h *ChatHistory) stepQuestionLocked(dir int, oldTop *int) {
	if h.buffer == nil {
		return
	}
	anchors := h.buffer.QuestionAnchors()
	if dir < 0 {
		idx := h.activeQuestionIndexLocked(anchors)
		if (len(anchors) == 0 || idx <= 0) && h.hasOlderLocked() {
			added := 0
			if h.readyPage != nil {
				added = h.applyOlderPageLocked()
			} else {
				added = h.loadOlderLocked()
			}
			if added > 0 {
				h.top += added
				if oldTop != nil {
					*oldTop += added
				}
			}
			if h.hasOlderLocked() {
				h.prefetchOlderLocked()
			}
			anchors = h.buffer.QuestionAnchors()
		}
	}
	if len(anchors) == 0 {
		return
	}
	idx := h.activeQuestionIndexLocked(anchors)
	if idx < 0 {
		idx = 0
	}
	targetIdx := idx
	if dir < 0 {
		if idx > 0 {
			targetIdx = idx - 1
		} else {
			targetIdx = len(anchors) - 1
		}
	} else {
		targetIdx = (idx + 1) % len(anchors)
	}
	minimum := h.buffer.FirstContentRow()
	if h.hasOlderLocked() {
		minimum = max(minimum, h.historyStart)
	}
	maxBrowsingTop := max(minimum, h.buffer.Base()-1)
	// Land one row past PromptRow so the sticky header pins the question on row 0
	// while the assistant response begins immediately on row 1 without duplicating "> <question>".
	h.top = min(maxBrowsingTop, max(minimum, anchors[targetIdx].PromptRow+1))
}

// Reserve only explicit history navigation while an approval is pending.
// In particular, Escape still cancels and Up/Down still move the selection.
func approvalHistoryNavigation(key string) bool {
	switch key {
	case "\x1b[5~", "\x1b[6~", "\x1b[1;5H", "\x1b[1;5F",
		"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~",
		"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~":
		return true
	}
	return false
}

// Search input stays out of the prompt so an unfinished message survives.
func (h *ChatHistory) filterSearchInput(key []byte) bool {
	if !h.reader.IsAtMainPrompt.Load() {
		return false
	}
	TerminalMu.Lock()
	action := string(key)
	if action == "\x06" {
		h.setSearchEditingLocked(true)
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
		h.setSearchEditingLocked(false)
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
		h.setSearchEditingLocked(false)
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
