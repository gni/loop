package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/lifecycle"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// AgentUIImpl implements agent.AgentUI and encapsulates the state of the TUI.
type AgentUIImpl struct {
	Config      *config.Config
	Theme       style.UITheme
	SessionID   string
	PPWriter    *PromptPreservingWriter
	ChatHistory *ChatHistory

	ActiveCancelFunc     context.CancelFunc
	PasteLinesOffset     int
	ActiveInputReader    io.Reader
	InApprovalPrompt     bool
	InInteractiveMenu    bool
	SessionReloaded      bool
	State                StatusBarState
	LastH                int
	LastPasteLinesOffset int
	Enabled              bool
	ScrollRegionOffset   int
	CollapseResults      bool
	LastStatsText        string
	ActivityStatsText    string
	LastStatusBarText    string
	PromptHint           string
	HoverTarget          render.HoverTarget
	SlashMenuOpen        bool
	PromptHitboxes       []render.Hitbox
	StatusHitbox         *render.Hitbox
	LastCtrlD            time.Time
	CtrlDTimer           *time.Timer

	StateMu sync.Mutex
}

// NewAgentUI initializes a new TUI instance and registers it as the active UI.
func NewAgentUI(cfg *config.Config, theme style.UITheme) *AgentUIImpl {
	uiImpl := &AgentUIImpl{
		Config:          cfg,
		Theme:           theme,
		CollapseResults: cfg.CollapseResults,
	}
	ActiveUI = uiImpl

	lifecycle.Default().OnCancel(func() bool {
		if IsInteractiveMode() {
			return uiImpl.CancelActiveOperation()
		}
		return false
	})
	lifecycle.Default().OnShutdown(func() {
		if h := uiImpl.GetChatHistory(); h != nil {
			h.Close()
		}
		uiImpl.ShutdownStatusBar(os.Stderr)
	})

	return uiImpl
}

func (ui *AgentUIImpl) GetPasteLinesOffset() int {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	return ui.PasteLinesOffset
}

func (ui *AgentUIImpl) SetPasteLinesOffset(offset int) {
	ui.StateMu.Lock()
	ui.PasteLinesOffset = offset
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) GetChatHistory() *ChatHistory {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	return ui.ChatHistory
}

func (ui *AgentUIImpl) SetChatHistory(h *ChatHistory) {
	ui.StateMu.Lock()
	ui.ChatHistory = h
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) ClearChatHistoryIf(h *ChatHistory) {
	ui.StateMu.Lock()
	if ui.ChatHistory == h {
		ui.ChatHistory = nil
	}
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) GetActiveInputReader() io.Reader {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	return ui.ActiveInputReader
}

func (ui *AgentUIImpl) SetActiveInputReader(r io.Reader) {
	ui.StateMu.Lock()
	ui.ActiveInputReader = r
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) CancelActiveOperation() bool {
	ui.StateMu.Lock()
	cancel := ui.ActiveCancelFunc
	ui.StateMu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}

func (ui *AgentUIImpl) SetCollapseStatus(collapsed bool) {
	ui.StateMu.Lock()
	ui.CollapseResults = collapsed
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) SetConfig(cfg *config.Config) {
	ui.StateMu.Lock()
	ui.Config = cfg
	hoverEnabled := true
	if cfg != nil {
		ui.Theme = style.ResolveConfiguredTheme(cfg.Theme, cfg.SyntaxTheme)
		ui.State.ShowTokens = cfg.ShowTokens
		ui.State.Model = cfg.Model
		ui.CollapseResults = cfg.CollapseResults
		hoverEnabled = cfg.HoverTooltips
		if !hoverEnabled {
			ui.HoverTarget = render.HoverNone
		}
	}
	ui.LastStatusBarText = ""
	ui.StateMu.Unlock()
	terminal.Default().SetMouseMotion(hoverEnabled)
}

// HitTestChrome checks 1-based terminal coordinates against active UI chrome hitboxes.
func (ui *AgentUIImpl) HitTestChrome(x, y int) render.HoverTarget {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if hit := render.HitTest(ui.PromptHitboxes, x, y); hit != render.HoverNone {
		return hit
	}
	if ui.StatusHitbox != nil && ui.StatusHitbox.Row == y && x >= ui.StatusHitbox.StartCol && x <= ui.StatusHitbox.EndCol {
		return ui.StatusHitbox.Target
	}
	return render.HoverNone
}

// SetHoverTarget updates the active hover target and reports whether it changed.
func (ui *AgentUIImpl) SetHoverTarget(next render.HoverTarget) (changed bool, prev render.HoverTarget) {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	prev = ui.HoverTarget
	if prev == next {
		return false, prev
	}
	ui.HoverTarget = next
	if prev == render.HoverCollapseBadge || next == render.HoverCollapseBadge {
		ui.LastStatusBarText = ""
	}
	return true, prev
}

// SetSlashMenuOpen records whether the inline slash-command dropdown is currently visible.
func (ui *AgentUIImpl) SetSlashMenuOpen(open bool) bool {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	changed := ui.SlashMenuOpen != open
	ui.SlashMenuOpen = open
	return changed
}

func (ui *AgentUIImpl) SetScrollRegionOffset(offset int) {
	ui.StateMu.Lock()
	ui.ScrollRegionOffset = offset
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) ClearScrollRegionOffset() {
	ui.StateMu.Lock()
	ui.ScrollRegionOffset = 0
	ui.StateMu.Unlock()
}

func (ui *AgentUIImpl) InitStatusBar(w io.Writer) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	_, height := GetTerminalSize()
	ui.StateMu.Lock()
	ui.Enabled = true
	// Shutdown clears the pinned rows. Identical status text must still be
	// painted when returning from a full-screen menu.
	ui.LastStatusBarText = ""
	ui.LastH = height
	ui.StateMu.Unlock()

	if height > 3 && w != nil {
		scrollBottom := style.ConsoleScrollBottom(height, 0)
		var buf bytes.Buffer
		buf.WriteString(terminal.BeginSyncOutput)
		// Print newline and cursor up to ensure we scroll if we are near the bottom
		buf.WriteString("\n\n\x1b[2A")
		// Save cursor, set scroll region, restore cursor to prevent jumping to top
		fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8", scrollBottom)
		buf.WriteString(terminal.EndSyncOutput)
		_, _ = w.Write(buf.Bytes())
	}
}

func (ui *AgentUIImpl) ShutdownStatusBar(w io.Writer) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()

	ui.StateMu.Lock()
	if !ui.Enabled {
		ui.StateMu.Unlock()
		return
	}
	ui.Enabled = false
	pasteOffset := max(0, ui.PasteLinesOffset)
	ui.StateMu.Unlock()

	if w == nil {
		return
	}
	_, height := GetTerminalSize()
	var buf bytes.Buffer
	buf.WriteString(terminal.BeginSyncOutput)
	startRow := max(1, height-4-pasteOffset)
	if height > 0 {
		for row := startRow; row <= height; row++ {
			fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", row)
		}
	}
	// Reset scrolling region (moves cursor to 1,1) and show cursor
	buf.WriteString("\x1b[r\x1b[?25h")
	if height > 4 {
		// Reposition cursor where the cleared UI controls began so goodbye
		// message and shell prompt appear cleanly right below history.
		fmt.Fprintf(&buf, "\x1b[%d;1H", startRow)
	} else if height > 0 {
		fmt.Fprintf(&buf, "\x1b[%d;1H", height)
	}
	buf.WriteString(terminal.EndSyncOutput)
	_, _ = w.Write(buf.Bytes())
}

// Implement the rest of agent.AgentUI interface by calling the package-level drawing functions.
func (ui *AgentUIImpl) DrawStatusBar(w io.Writer, theme style.UITheme) {
	DrawStatusBar(w, theme)
}

func (ui *AgentUIImpl) DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string) {
	DrawStaticPromptSeparatorWithSpinner(w, showThinking, reasoningEffort, theme, spinnerFrame)
}

func (ui *AgentUIImpl) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) agent.StreamRenderer {
	ui.StateMu.Lock()
	options := render.StreamOptions{Markdown: true}
	if ui.Config != nil {
		options.Markdown = ui.Config.MarkdownRender
		options.ShowMicroseconds = ui.Config.ShowMicroseconds
	}
	ui.StateMu.Unlock()
	options.BeforeWrite = func() {
		ui.StateMu.Lock()
		restore := ui.PasteLinesOffset > 0
		ui.StateMu.Unlock()
		if !restore {
			return
		}
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		ui.StateMu.Lock()
		if ui.PasteLinesOffset == 0 {
			ui.StateMu.Unlock()
			return
		}
		ui.PasteLinesOffset = 0
		ui.LastStatusBarText = ""
		history := ui.ChatHistory
		pp := ui.PPWriter
		inMenu := ui.InInteractiveMenu
		ui.StateMu.Unlock()

		width, height := GetTerminalSize()
		if history != nil && !history.closed.Load() {
			history.resizeLocked(width, height)
		} else if pp != nil {
			pp.ResizeLocked(height)
		}
		if height > 3 && !inMenu && !terminal.Default().InAltScreen() {
			scrollBottom := style.ConsoleScrollBottom(height, 0)
			var buf bytes.Buffer
			buf.WriteString(terminal.BeginSyncOutput)
			fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8", scrollBottom)
			buf.WriteString(terminal.EndSyncOutput)
			_, _ = os.Stderr.Write(buf.Bytes())
		}
	}
	sr := render.NewStreamRenderer(w, theme, showThinking, streamWrites, options)
	return sr
}

func (ui *AgentUIImpl) UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool) {
	// The post-turn context occupancy is inferred from provider usage and
	// retained history; only the provider's per-request usage is exact.
	UpdateStatus(model, promptTokens, completionTokens, currentCompletionTokens, contextLimit, isGenerating, tps, activeTasks, showTokens, true)
}

func (ui *AgentUIImpl) UpdatePlanStatus(completed, total int) {
	UpdatePlanStatus(completed, total)
}

func (ui *AgentUIImpl) DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string) {
	DrawStaticStatsLine(w, theme, spinnerFrame, statsText)
}

// DrawActivityStatsLine scopes a temporary activity label independently of the
// last completed turn. Clearing it restores the saved status on every redraw.
func (ui *AgentUIImpl) DrawActivityStatsLine(w io.Writer, theme style.UITheme, text string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	ui.StateMu.Lock()
	ui.ActivityStatsText = text
	saved := ui.LastStatsText
	ui.StateMu.Unlock()
	DrawStaticStatsLineLocked(w, theme, "", saved)
}

func (ui *AgentUIImpl) AskForApproval(w io.Writer, theme style.UITheme, allowSession bool) (bool, bool) {
	return AskForApproval(w, theme, allowSession)
}

func (ui *AgentUIImpl) AskUserQuestion(w io.Writer, theme style.UITheme, question string, options []string, recommended string) (string, error) {
	return AskUserQuestion(w, theme, question, options, recommended)
}

func (ui *AgentUIImpl) AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) agent.SubagentCancellationDecision {
	return AskForSubagentCancellation(w, theme, agentName)
}

func (ui *AgentUIImpl) RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string) {
	render.RenderToolHeader(w, theme, toolName, toolArgs)
}

func (ui *AgentUIImpl) RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool) {
	render.RenderToolOutput(w, output, isError, collapseResults, theme, toolName, toolArgs, bodyWasStreamed)
}

func (ui *AgentUIImpl) SetCursorHidden(hidden bool) {
	if ui.PPWriter != nil {
		ui.PPWriter.SetCursorHidden(hidden)
	}
}

func (ui *AgentUIImpl) GetPPWriter() io.Writer {
	if ui.PPWriter != nil {
		return ui.PPWriter
	}
	return nil
}

func (ui *AgentUIImpl) RenderGenerationError(w io.Writer, message string, theme style.UITheme) {
	ui.StateMu.Lock()
	collapse := ui.CollapseResults
	ui.StateMu.Unlock()
	render.RenderFailure(w, message, collapse, theme)
}

// SetPromptHint sets a transient hint message to be displayed at the prompt,
// optionally scheduling a timer to clear the hint and call onExpire if not cancelled.
func (ui *AgentUIImpl) SetPromptHint(hint string, d time.Duration, onExpire func()) {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if ui.CtrlDTimer != nil {
		ui.CtrlDTimer.Stop()
		ui.CtrlDTimer = nil
	}
	ui.PromptHint = hint
	ui.LastCtrlD = time.Now()
	if d > 0 && onExpire != nil {
		targetTime := ui.LastCtrlD
		ui.CtrlDTimer = time.AfterFunc(d, func() {
			ui.StateMu.Lock()
			if ui.LastCtrlD.Equal(targetTime) {
				ui.PromptHint = ""
				ui.LastCtrlD = time.Time{}
				ui.CtrlDTimer = nil
				ui.StateMu.Unlock()
				onExpire()
			} else {
				ui.StateMu.Unlock()
			}
		})
	}
}

// ClearPromptHint cancels any active prompt hint and timer immediately.
func (ui *AgentUIImpl) ClearPromptHint() {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if ui.CtrlDTimer != nil {
		ui.CtrlDTimer.Stop()
		ui.CtrlDTimer = nil
	}
	ui.PromptHint = ""
	ui.LastCtrlD = time.Time{}
}

// CheckCtrlDConfirmation checks if Ctrl+D was pressed within the timeout window.
// If confirmed, it clears the hint state and returns true.
func (ui *AgentUIImpl) CheckCtrlDConfirmation(timeout time.Duration) bool {
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	if !ui.LastCtrlD.IsZero() && time.Since(ui.LastCtrlD) <= timeout {
		if ui.CtrlDTimer != nil {
			ui.CtrlDTimer.Stop()
			ui.CtrlDTimer = nil
		}
		ui.PromptHint = ""
		ui.LastCtrlD = time.Time{}
		return true
	}
	return false
}

func syncAgentStatus(a *agent.Agent, theme style.UITheme, calcHistoryTokens func() (int, int, bool)) {
	pTok, cTok, estimated := calcHistoryTokens()
	effLimit := a.GetEffectiveContextLimit(pTok)
	live := a.LiveSettings()
	UpdateStatus(live.Model, pTok, cTok, 0, effLimit, false, 0, GetActiveTasks(a), live.ShowTokens, estimated)
	DrawStatusBar(os.Stderr, theme)
}
