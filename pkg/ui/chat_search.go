package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

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
	row := h.buffer.FindRow(h.searchQuery, h.searchRow+direction, direction)
	if row >= 0 {
		h.showSearchRowLocked(row)
		return nil
	}
	if h.hasOlderLocked() {
		h.searchPending = true
		h.continueSearchLocked()
		return nil
	}
	h.wrapSearchLocked()
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
	row := h.buffer.FindRow(h.searchQuery, from, h.searchDirection)
	if row >= 0 {
		h.showSearchRowLocked(row)
		return
	}
	h.searchPending = false
	h.searchNotice = "No matches: " + h.searchQuery
	h.paintLocked()
}

func (h *ChatHistory) continueSearchLocked() {
	if !h.searchPending {
		return
	}
	if h.readyPage != nil {
		added := h.applyOlderPageLocked()
		if added > 0 && h.searchDirection < 0 {
			if row := h.buffer.FindRow(h.searchQuery, added-1, -1); row >= 0 {
				h.showSearchRowLocked(row)
				return
			}
		}
	}
	if !h.hasOlderLocked() {
		h.wrapSearchLocked()
		return
	}
	h.prefetchOlderLocked()
	h.paintLocked()
}

func (h *ChatHistory) searchRowsLocked(rows []string) []string {
	if h.searchQuery != "" && h.searchRow >= h.top && h.searchRow < h.top+len(rows) {
		index := h.searchRow - h.top
		rows[index] = style.NewStyle().Foreground(GetConfiguredTheme(h.a.Config).Highlight).Underline(true).Render(style.StripAnsi(rows[index]))
	}
	return rows
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

func (h *ChatHistory) searchMatchNoticeLocked() string {
	return fmt.Sprintf("Find: %s · line %d · Ctrl+N/P: next/prev · End: latest", h.searchQuery, h.searchRow+1)
}
