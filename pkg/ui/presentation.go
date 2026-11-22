package ui

import (
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func PrintSessionHistory(w io.Writer, messages []db.Message, theme UITheme, cfg *config.Config) {
	ui := getUI()
	isGen := false
	collapse := false
	if ui != nil {
		ui.StateMu.Lock()
		isGen = ui.State.IsGenerating || ui.ActiveCancelFunc != nil
		collapse = ui.CollapseResults
		ui.StateMu.Unlock()
	}
	hooks := render.HistoryRenderHooks{
		RenderMarkdown:  render.RenderMarkdownContent,
		RenderReasoning: render.RenderReasoningMarkdownContent,
	}
	if cfg != nil && !cfg.MarkdownRender {
		hooks.RenderMarkdown = render.RenderRawMarkdownContent
	}
	render.PrintSessionHistory(w, messages, theme, cfg, isGen, collapse, hooks)
}

func getWriterHeight(w io.Writer) int {
	type heightGetter interface {
		Height() int
	}
	type wrapper interface {
		Unwrap() io.Writer
	}

	curr := w
	for curr != nil {
		if hg, ok := curr.(heightGetter); ok {
			return hg.Height()
		}
		if wr, ok := curr.(wrapper); ok {
			curr = wr.Unwrap()
		} else {
			break
		}
	}
	return 0
}

func DrawStaticStatsLine(w io.Writer, theme UITheme, spinnerFrame string, statsText string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStaticStatsLineLocked(w, theme, spinnerFrame, statsText)
}

func DrawStaticStatsLineLocked(w io.Writer, theme UITheme, spinnerFrame string, statsText string) {
	ui := getUI()
	inAlt := terminal.Default().InAltScreen()
	ui.StateMu.Lock()
	if spinnerFrame == "" && statsText != "" {
		clean := style.StripAnsi(statsText)
		if !strings.HasPrefix(clean, "(") || strings.Contains(clean, "out •") {
			ui.LastStatsText = statsText
		}
	}
	blocked := !ui.Enabled || ui.InApprovalPrompt || ui.InInteractiveMenu || inAlt
	textToDraw := statsText
	if ui.ActivityStatsText != "" {
		textToDraw = ui.ActivityStatsText
	}
	pasteLinesOffset := ui.PasteLinesOffset
	history := ui.ChatHistory
	ui.StateMu.Unlock()
	if blocked {
		return
	}

	height := getWriterHeight(w)
	if height <= 0 {
		_, h := GetTerminalSize()
		height = h
	}

	if history != nil && !history.closed.Load() && history.drawnHint != "" {
		textToDraw = history.drawnHint
		spinnerFrame = ""
	}
	render.DrawStaticStatsLine(agent.UnwrapWriter(w), theme, spinnerFrame, textToDraw, height, pasteLinesOffset)
}

func DrawStaticPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStaticPromptSeparatorLocked(w, showThinking, reasoningEffort, theme)
}

func DrawStaticPromptSeparatorLocked(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme) {
	DrawStaticPromptSeparatorWithSpinnerLocked(w, showThinking, reasoningEffort, theme, "")
}

func DrawStaticPromptSeparatorWithSpinner(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	TerminalMu.Lock()
	defer TerminalMu.Unlock()
	DrawStaticPromptSeparatorWithSpinnerLocked(w, showThinking, reasoningEffort, theme, spinnerFrame)
}

func DrawStaticPromptSeparatorWithSpinnerLocked(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	ui := getUI()
	inAlt := terminal.Default().InAltScreen()
	ui.StateMu.Lock()
	blocked := !ui.Enabled || ui.InApprovalPrompt || ui.InInteractiveMenu || inAlt
	pasteLinesOffset := ui.PasteLinesOffset
	showReasoning := true
	showBranch := true
	showAutoBadge := true
	autoApprove := false
	slashMenuOpen := ui.SlashMenuOpen
	hovered := render.HoverNone
	if ui.Config != nil {
		showReasoning = ui.Config.ShowReasoningBadge
		showBranch = ui.Config.ShowGitBranch
		showAutoBadge = ui.Config.ShowAutoBadge
		autoApprove = ui.Config.AutoApprove
		if ui.Config.HoverTooltips {
			hovered = ui.HoverTarget
		}
	}
	ui.StateMu.Unlock()
	if blocked {
		return
	}

	height := getWriterHeight(w)
	if height <= 0 {
		_, h := GetTerminalSize()
		height = h
	}

	width, _ := GetTerminalSize()
	branch := ""
	if showBranch {
		branch = CachedGitBranch()
	}
	if !showReasoning {
		reasoningEffort = "hidden"
	}
	hitboxes := render.DrawStaticPromptSeparatorWithHover(agent.UnwrapWriter(w), showThinking, reasoningEffort, showAutoBadge, autoApprove, slashMenuOpen, hovered, theme, width, height, pasteLinesOffset, branch)
	ui.StateMu.Lock()
	ui.PromptHitboxes = hitboxes
	ui.StateMu.Unlock()
}
