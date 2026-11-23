package history

import (
	"strings"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func parseSGRMouse(key []byte) (button, x, y int, release, ok bool) {
	if len(key) < 9 || key[0] != 0x1b || key[1] != '[' || key[2] != '<' {
		return 0, 0, 0, false, false
	}
	last := key[len(key)-1]
	if last != 'M' && last != 'm' {
		return 0, 0, 0, false, false
	}
	release = last == 'm'
	vals := [3]int{}
	idx := 0
	hasDigit := false
	for i := 3; i < len(key)-1; i++ {
		c := key[i]
		if c >= '0' && c <= '9' {
			vals[idx] = vals[idx]*10 + int(c-'0')
			hasDigit = true
		} else if c == ';' && hasDigit && idx < 2 {
			idx++
			hasDigit = false
		} else {
			return 0, 0, 0, false, false
		}
	}
	if idx != 2 || !hasDigit {
		return 0, 0, 0, false, false
	}
	return vals[0], vals[1], vals[2], release, true
}

func isPromptHoverTarget(t render.HoverTarget) bool {
	return t == render.HoverCommandsButton || t == render.HoverThemeButton || t == render.HoverAutoBadge || t == render.HoverReasoningBadge || t == render.HoverGitBranch
}

func (h *ChatHistory) handleHoverMotion(x, y int) {
	if h.handleSlashMenuHover(x, y) {
		state := ui.GetUI()
		if changed, prev := state.SetHoverTarget(render.HoverNone); changed && h.a != nil && h.reader != nil && h.reader.W != nil {
			live := h.a.LiveSettings()
			theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
			ui.TerminalMu.Lock()
			if isPromptHoverTarget(prev) {
				ui.DrawStaticPromptSeparatorLocked(h.reader.W, live.ShowThinking, live.ReasoningEffort, theme)
			}
			if prev == render.HoverCollapseBadge {
				ui.DrawStatusBarLocked(h.reader.W, theme)
			}
			ui.TerminalMu.Unlock()
		}
		return
	}
	if h.a != nil && !h.a.LiveSettings().HoverTooltips {
		return
	}
	state := ui.GetUI()
	next := state.HitTestChrome(x, y)
	changed, prev := state.SetHoverTarget(next)
	if !changed {
		return
	}
	if h.a == nil || h.reader == nil || h.reader.W == nil {
		return
	}
	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	if isPromptHoverTarget(prev) || isPromptHoverTarget(next) {
		ui.DrawStaticPromptSeparatorLocked(h.reader.W, live.ShowThinking, live.ReasoningEffort, theme)
	}
	if prev == render.HoverCollapseBadge || next == render.HoverCollapseBadge {
		ui.DrawStatusBarLocked(h.reader.W, theme)
	}
}

func (h *ChatHistory) handleChromeClick(target render.HoverTarget) (bool, []byte) {
	if h.a == nil || h.reader == nil {
		return false, nil
	}
	switch target {
	case render.HoverCommandsButton:
		return true, h.toggleCommandsButton()
	case render.HoverThemeButton:
		h.reader.HandleCtrlY()
		return true, nil
	case render.HoverAutoBadge:
		cfg, err := h.a.Configuration()
		if err != nil || cfg == nil {
			return false, nil
		}
		cfg.AutoApprove = !cfg.AutoApprove
		if err := h.a.UpdateConfig(cfg); !agent.ConfigApplied(err) {
			return false, nil
		}
		_ = h.a.SaveConfig()
		live := h.a.LiveSettings()
		theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
		ui.TerminalMu.Lock()
		ui.DrawStaticPromptSeparatorLocked(h.reader.W, live.ShowThinking, live.ReasoningEffort, theme)
		ui.TerminalMu.Unlock()
		return true, nil
	case render.HoverReasoningBadge:
		order := []string{"off", "low", "medium", "high", "max"}
		if err := h.a.UpdateLiveSettings(func(next *agent.LiveSettings) {
			cur := strings.ToLower(strings.TrimSpace(next.ReasoningEffort))
			if cur == "" || cur == "none" {
				cur = "off"
			}
			nextIdx := 1
			for i, v := range order {
				if v == cur {
					nextIdx = (i + 1) % len(order)
					break
				}
			}
			next.ReasoningEffort = order[nextIdx]
		}); err != nil {
			return false, nil
		}
		_ = h.a.SaveConfig()
		live := h.a.LiveSettings()
		theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
		ui.TerminalMu.Lock()
		ui.DrawStaticPromptSeparatorLocked(h.reader.W, live.ShowThinking, live.ReasoningEffort, theme)
		ui.TerminalMu.Unlock()
		return true, nil
	case render.HoverCollapseBadge:
		if err := h.a.UpdateLiveSettings(func(next *agent.LiveSettings) {
			next.CollapseResults = !next.CollapseResults
		}); err != nil {
			return false, nil
		}
		_ = h.a.SaveConfig()
		live := h.a.LiveSettings()
		ui.SetCollapseStatus(live.CollapseResults)
		theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
		ui.TerminalMu.Lock()
		ui.GetUI().StateMu.Lock()
		ui.GetUI().LastStatusBarText = ""
		ui.GetUI().StateMu.Unlock()
		ui.DrawStatusBarLocked(h.reader.W, theme)
		if h.reader.ActiveTurn.Load() {
			h.reader.PendingCollapseToggles.Add(1)
		} else if !h.closed.Load() {
			h.drawn = nil
			h.paintLocked()
		}
		ui.TerminalMu.Unlock()
		return true, nil
	default:
		return false, nil
	}
}

func (h *ChatHistory) FilterInput(key []byte) []byte {
	if len(key) == 0 || h.closed.Load() {
		return key
	}
	// Fast-path Ctrl+C outside search editing without acquiring ui.TerminalMu so
	// cancellation is never stalled behind heavy disk or Chroma rendering.
	if len(key) == 1 && key[0] == 3 && !h.searchEditingAtomic.Load() {
		return key
	}
	action := string(key)
	mouse := strings.HasPrefix(action, "\x1b[<") || strings.HasPrefix(action, "\x1b[M")
	ui.GetUI().StateMu.Lock()
	approval := ui.GetUI().InApprovalPrompt
	modal := ui.GetUI().InInteractiveMenu || terminal.Default().InAltScreen()
	ui.GetUI().StateMu.Unlock()
	if modal {
		return key
	}
	if !approval && !h.reader.ActiveTurn.Load() && !h.reader.IsAtMainPrompt.Load() {
		if mouse {
			return nil
		}
		return key
	}
	// Fast-path SGR/X10 mouse motion, chrome clicks, and slash menu interactions without acquiring ui.TerminalMu on ordinary moves.
	if mouse && !approval {
		if btn, mx, my, release, ok := parseSGRMouse(key); ok {
			if release {
				return nil
			}
			if btn&32 != 0 {
				h.handleHoverMotion(mx, my)
				return nil
			}
			switch btn & 0xc3 {
			case 0:
				if hit := ui.GetUI().HitTestChrome(mx, my); hit != render.HoverNone {
					if handled, emit := h.handleChromeClick(hit); handled {
						return emit
					}
				}
				if handled, emit := h.handleSlashMenuClick(mx, my); handled {
					return emit
				}
			case 64:
				if h.handleSlashMenuWheel(-1, mx, my) {
					return nil
				}
			case 65:
				if h.handleSlashMenuWheel(1, mx, my) {
					return nil
				}
			}
		} else if len(key) == 6 && strings.HasPrefix(action, "\x1b[M") && key[3] >= 32 {
			btn := (key[3] - 32) & 0xc3
			mx := int(key[4]) - 32
			my := int(key[5]) - 32
			switch btn {
			case 0:
				if hit := ui.GetUI().HitTestChrome(mx, my); hit != render.HoverNone {
					if handled, emit := h.handleChromeClick(hit); handled {
						return emit
					}
				}
				if handled, emit := h.handleSlashMenuClick(mx, my); handled {
					return emit
				}
			case 64:
				if h.handleSlashMenuWheel(-1, mx, my) {
					return nil
				}
			case 65:
				if h.handleSlashMenuWheel(1, mx, my) {
					return nil
				}
			}
		}
	}
	// Approval arrows and shortcuts belong to the choice menu. Bring it back
	// into view before forwarding input; scroll keys must never select a choice.
	if approval && !mouse && !approvalHistoryNavigation(action) {
		ui.TerminalMu.Lock()
		browsing := h.browsing
		ui.TerminalMu.Unlock()
		if browsing {
			h.Latest()
		}
		return key
	}
	if !approval && h.filterSearchInput(key) {
		return nil
	}
	if !approval && !mouse && h.slashMenuOpenAtomic.Load() {
		switch action {
		case "\r":
			if handled, emit := h.handleSlashMenuEnter(); handled {
				return emit
			}
		case "\x1b":
			if handled, emit := h.handleSlashMenuEscape(); handled {
				return emit
			}
		}
	}
	ui.TerminalMu.Lock()
	defer ui.TerminalMu.Unlock()
	h.resizeLocked(ui.GetTerminalSize())
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
		if button, x, y, release, ok := parseSGRMouse(key); ok {
			if !release && button&32 == 0 {
				if button&0xc3 == 0 && h.browsing {
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
				switch button & 0xc3 {
				case 64:
					delta = -3
				case 65:
					delta = 3
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
	ui.TerminalMu.Lock()
	action := string(key)
	if action == "\x06" {
		h.setSearchEditingLocked(true)
		h.searchDraft = h.searchQuery
		h.searchPending = false
		h.searchScan = nil
		h.paintLocked()
		ui.TerminalMu.Unlock()
		return true
	}
	if !h.searchEditing {
		query := h.searchQuery
		ui.TerminalMu.Unlock()
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
		ui.TerminalMu.Unlock()
		if query != "" {
			_ = h.Find(query, 0)
		} else {
			ui.TerminalMu.Lock()
			h.searchQuery, h.searchNotice = "", ""
			h.searchRow = -1
			h.searchPending = false
			h.searchScan = nil
			h.paintLocked()
			ui.TerminalMu.Unlock()
		}
		return true
	case "\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~", "\x1b[1;5F":
		ui.TerminalMu.Unlock()
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
	ui.TerminalMu.Unlock()
	return true
}
