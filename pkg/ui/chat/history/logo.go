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
		ticker := time.NewTicker(logo.FrameInterval)
		defer ticker.Stop()
		for {
			select {
			case <-h.logoStop:
				return
			case <-ticker.C:
				var (
					ready    bool
					cols     int
					height   int
					bufWidth int
					numRows  int
					next     int
					resetSeq uint64
					app      logo.Appearance
				)
				ui.TerminalMu.Lock()
				if h.canAnimateLogoLocked() {
					rows := h.buffer.View(h.top)
					cfg := h.logoConfig()
					if h.canShowLogoLocked(rows) && !cfg.ReducedMotion {
						bufWidth = h.buffer.Width
						numRows = len(rows)
						cols, height = logoViewportBounds(bufWidth, numRows)
						next = h.logoFrame + 1
						if next >= logo.CycleFrames {
							next = logo.EntranceFrames
						}
						resetSeq = h.resetSequence.Load()
						app = logoAppearance(cfg)
						ready = true
					}
				}
				ui.TerminalMu.Unlock()

				if !ready {
					continue
				}

				// Raymarch the 3D frame outside ui.TerminalMu so keyboard input stays instant.
				rendered := logo.Render(cols, height, next, app)

				ui.TerminalMu.Lock()
				if h.resetSequence.Load() == resetSeq && h.canAnimateLogoLocked() {
					rows := h.buffer.View(h.top)
					cfg := h.logoConfig()
					if h.canShowLogoLocked(rows) && !cfg.ReducedMotion &&
						h.buffer.Width == bufWidth && len(rows) == numRows {
						h.logoFrame = next
						h.logoVisible = true
						applyLogoRows(rows, rendered, bufWidth, cols, height)
						if h.shouldRenderSlashMenuLocked() {
							rows = h.decorateSlashMenuLocked(rows)
						}
						render.DrawChatViewportOverlay(h.reader.W, rows, h.drawn)
						h.drawn = rows
					}
				}
				ui.TerminalMu.Unlock()
			}
		}
	}()
}

func (h *ChatHistory) canAnimateLogoLocked() bool {
	if h.closed.Load() || h.captureOnly || h.isNativeScroll() || !h.reader.IsAtMainPrompt.Load() ||
		h.reader.ActiveTurn.Load() || h.reader.HasPromptDraft() || h.slashMenuOpen {
		return false
	}
	state := ui.GetUI()
	state.StateMu.Lock()
	modal := state.InApprovalPrompt || state.InInteractiveMenu || terminal.Default().InAltScreen()
	state.StateMu.Unlock()
	return !modal
}

func logoViewportBounds(bufWidth, numRows int) (cols, height int) {
	cols = min(72, max(1, bufWidth-4))
	maxRows := numRows
	if maxRows > 6 {
		maxRows -= 2
	}
	height = min(19, max(1, maxRows))
	return cols, height
}

func logoAppearance(cfg config.Config) logo.Appearance {
	theme := style.ResolveConfiguredTheme(cfg.Theme, cfg.SyntaxTheme)
	return logo.Appearance{
		Brightness: cfg.LogoBrightness,
		Light:      style.IsLightTheme(cfg.Theme),
		Primary:    theme.Primary,
		Secondary:  theme.Secondary,
		Highlight:  theme.Highlight,
		TextMuted:  theme.TextMuted,
		Background: theme.Background,
	}
}

func applyLogoRows(rows, rendered []string, bufWidth, cols, height int) {
	top := (len(rows) - height) / 2
	left := max(0, (bufWidth-cols)/2)
	pad := strings.Repeat(" ", left)
	for row, line := range rendered {
		if top+row >= 0 && top+row < len(rows) {
			rows[top+row] = pad + line
		}
	}
}

func (h *ChatHistory) canShowLogoLocked(rows []string) bool {
	if !h.logoEnabled || h.isNativeScroll() || !h.logoConfig().Logo || h.captureOnly || h.browsing || h.buffer.Base() != 0 || h.reader.ActiveTurn.Load() || h.hasOlderLocked() {
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
		cols, height := logoViewportBounds(h.buffer.Width, len(rows))
		cfg := h.logoConfig()
		frame := h.logoFrame
		if cfg.ReducedMotion {
			frame = logo.ReducedFrame
		}
		rendered := logo.Render(cols, height, frame, logoAppearance(cfg))
		applyLogoRows(rows, rendered, h.buffer.Width, cols, height)
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
