package history

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/chat/logo"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// The logo is a viewport decoration, never a saved chat row or a separate screen.
func (h *ChatHistory) startChatLogo() {
	file, ok := h.reader.W.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	h.logoEnabled = true
	h.logoStop = make(chan struct{})
	h.logoDone = make(chan struct{})
	go func() {
		defer close(h.logoDone)
		ticker := time.NewTicker(60 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-h.logoStop:
				return
			case <-ticker.C:
				ui.TerminalMu.Lock()
				if !h.closed.Load() && !h.captureOnly && h.reader.IsAtMainPrompt.Load() && !h.reader.ActiveTurn.Load() && !h.reader.HasPromptDraft() && !h.slashMenuOpen {
					state := ui.GetUI()
					state.StateMu.Lock()
					modal := state.InApprovalPrompt || state.InInteractiveMenu || terminal.Default().InAltScreen()
					state.StateMu.Unlock()
					if !modal {
						rows := h.buffer.View(h.top)
						if h.canShowLogoLocked(rows) && !h.logoConfig().ReducedMotion {
							h.logoFrame++
							if h.logoFrame >= logo.CycleFrames {
								h.logoFrame = logo.EntranceFrames
							}
							rows = h.decorateChatLocked(rows)
							render.DrawChatViewport(h.reader.W, rows, h.drawn, h.drawnHint, h.drawnHint)
							h.drawn = rows
						}
					}
				}
				ui.TerminalMu.Unlock()
			}
		}
	}()
}

func (h *ChatHistory) canShowLogoLocked(rows []string) bool {
	if !h.logoEnabled || !h.logoConfig().Logo || h.captureOnly || h.browsing || h.buffer.Base() != 0 || h.reader.ActiveTurn.Load() || h.hasOlderLocked() {
		return false
	}
	for _, row := range rows {
		if strings.TrimSpace(style.StripAnsi(row)) != "" {
			return false
		}
	}
	return len(rows) > 0
}

func (h *ChatHistory) decorateChatLocked(rows []string) []string {
	h.logoVisible = h.canShowLogoLocked(rows)
	if h.logoVisible {
		cols := min(74, max(1, h.buffer.Width-2))
		height := min(19, len(rows))
		cfg := h.logoConfig()
		frame := h.logoFrame
		if cfg.ReducedMotion {
			frame = logo.ReducedFrame
		}
		theme := style.ResolveConfiguredTheme(cfg.Theme, cfg.SyntaxTheme)
		rendered := logo.Render(cols, height, frame, logo.Appearance{
			Brightness: cfg.LogoBrightness,
			Light:      cfg.Theme == "light",
			Primary:    theme.Primary,
			Highlight:  theme.Highlight,
		})
		top, left := (len(rows)-height)/2, max(0, (h.buffer.Width-cols)/2)
		for row, line := range rendered {
			rows[top+row] = strings.Repeat(" ", left) + line
		}
	} else if h.browsing && len(rows) > 0 && h.a != nil && h.a.LiveSettings().ShowStickyQuestion {
		rows = h.decorateStickyQuestionLocked(rows)
	}
	if h.shouldRenderSlashMenuLocked() {
		rows = h.decorateSlashMenuLocked(rows)
	}
	return rows
}

func (h *ChatHistory) activeQuestionIndexLocked(anchors []render.QuestionAnchor) int {
	if len(anchors) == 0 {
		return -1
	}
	// When the first question is visible at the top of the chat viewport,
	// do not show the sticky bar so the first question is never duplicated.
	if h.top <= anchors[0].PromptRow {
		return -1
	}
	idx := -1
	for i, a := range anchors {
		if a.PromptRow < h.top {
			idx = i
		} else {
			break
		}
	}
	return idx
}

func (h *ChatHistory) decorateStickyQuestionLocked(rows []string) []string {
	if h.buffer == nil || len(rows) == 0 {
		return rows
	}
	anchors := h.buffer.QuestionAnchors()
	if len(anchors) == 0 {
		return rows
	}
	idx := h.activeQuestionIndexLocked(anchors)
	if idx < 0 || idx >= len(anchors) {
		return rows
	}

	live := h.a.LiveSettings()
	theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	available := max(1, h.buffer.Width-1)

	totalSuffix := ""
	if h.hasOlderLocked() {
		totalSuffix = "+"
	}
	badgeRaw := fmt.Sprintf(" [↑ %d/%d%s ↓]", idx+1, len(anchors), totalSuffix)
	badgeWidth := style.CellWidth(badgeRaw)

	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.TextMuted)
	textStyle := style.NewStyle().Foreground(theme.TextMuted)
	badgeStyle := style.NewStyle().Foreground(theme.TextMuted)

	cleanQ := strings.Join(strings.Fields(render.SanitizeTerminalText(anchors[idx].Text)), " ")
	if cleanQ == "" {
		cleanQ = "..."
	}

	prefixRaw := "─── ▹ "
	prefixWidth := style.CellWidth(prefixRaw)

	if available <= badgeWidth+prefixWidth+4 {
		out := append([]string(nil), rows...)
		out[0] = textStyle.Render(style.TruncateCells(prefixRaw+cleanQ, available))
		return out
	}

	maxQWidth := available - prefixWidth - badgeWidth - 3
	if style.CellWidth(cleanQ) > maxQWidth {
		if maxQWidth > 1 {
			cleanQ = style.TruncateCells(cleanQ, maxQWidth-1) + "…"
		} else {
			cleanQ = style.TruncateCells(cleanQ, maxQWidth)
		}
	}
	qWidth := style.CellWidth(cleanQ)
	dashCount := max(1, available-prefixWidth-qWidth-badgeWidth-1)

	stickyLine := borderStyle.Render("─── ") + promptStyle.Render("▹ ") +
		textStyle.Render(cleanQ) +
		" " + borderStyle.Render(strings.Repeat("─", dashCount)) +
		badgeStyle.Render(badgeRaw)

	out := append([]string(nil), rows...)
	out[0] = stickyLine
	return out
}

func (h *ChatHistory) logoConfig() config.Config {
	if h.a != nil {
		h.a.LLMProviderMu.RLock()
		if h.a.Config != nil {
			cfg := *h.a.Config
			h.a.LLMProviderMu.RUnlock()
			return cfg
		}
		h.a.LLMProviderMu.RUnlock()
	}
	state := ui.GetUI()
	state.StateMu.Lock()
	var cfg config.Config
	if state.Config != nil {
		cfg = *state.Config
	}
	state.StateMu.Unlock()
	return cfg
}

// Clear the decoration before foreground output reaches the chat writer.
func (h *ChatHistory) clearChatLogoLocked() {
	if !h.logoVisible {
		return
	}
	rows := h.buffer.View(h.top)
	render.DrawChatViewport(h.reader.W, rows, h.drawn, h.drawnHint, h.drawnHint)
	h.drawn = rows
	h.logoVisible = false
}
