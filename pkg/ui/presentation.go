package ui

import (
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
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
	height := getWriterHeight(w)
	if height <= 0 {
		_, h := GetTerminalSize()
		height = h
	}

	getUI().StateMu.Lock()
	if spinnerFrame == "" {
		if statsText != "" {
			clean := style.StripAnsi(statsText)
			if !strings.HasPrefix(clean, "(") || strings.Contains(clean, "out •") {
				getUI().LastStatsText = statsText
			}
		}
	}
	textToDraw := statsText
	if getUI().ActivityStatsText != "" {
		textToDraw = getUI().ActivityStatsText
	}
	pasteLinesOffset := getUI().PasteLinesOffset
	getUI().StateMu.Unlock()

	if history := getUI().ChatHistory; history != nil && !history.closed.Load() && history.drawnHint != "" {
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
	height := getWriterHeight(w)
	if height <= 0 {
		_, h := GetTerminalSize()
		height = h
	}

	width, _ := GetTerminalSize()
	getUI().StateMu.Lock()
	pasteLinesOffset := getUI().PasteLinesOffset
	getUI().StateMu.Unlock()
	branch := CachedGitBranch()
	render.DrawStaticPromptSeparator(agent.UnwrapWriter(w), showThinking, reasoningEffort, theme, width, height, pasteLinesOffset, branch)
}
