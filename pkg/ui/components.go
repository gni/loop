package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

type toolRenderStatus = render.ToolRenderStatus

const (
	toolStatusPending = render.ToolStatusPending
	toolStatusSuccess = render.ToolStatusSuccess
	toolStatusError   = render.ToolStatusError
)

type PromptLayout = render.PromptLayout

func PrintBanner(w io.Writer, a *agent.Agent) {
	if a == nil || a.Config == nil {
		return
	}
	render.PrintBanner(w, a, GetConfiguredTheme(a.Config))
}

func RenderConfig(w io.Writer, cfg *config.Config, theme UITheme) {
	render.RenderConfig(w, cfg, theme)
}

func RenderProviders(w io.Writer, cfg *config.Config, theme UITheme) {
	render.RenderProviders(w, cfg, theme)
}

func RenderMCPServers(w io.Writer, cfg *config.Config, theme UITheme) {
	render.RenderMCPServers(w, cfg, theme)
}

func RenderMCPStartupErrors(w io.Writer, startErrors map[string]error, theme UITheme) {
	render.RenderMCPStartupErrors(w, startErrors, theme)
}

func RenderToolHeader(w io.Writer, theme UITheme, toolName string, argsJSON string) {
	render.RenderToolHeader(w, theme, toolName, argsJSON)
}

func RenderToolOutput(w io.Writer, output string, isError bool, collapse bool, theme UITheme, toolName string, argsJSON string, bodyWasStreamed bool) {
	render.RenderToolOutput(w, output, isError, collapse, theme, toolName, argsJSON, bodyWasStreamed)
}

func CalculatePromptLayout(promptPrefix string, inputLine string, pos int, termWidth int) PromptLayout {
	return render.CalculatePromptLayout(promptPrefix, inputLine, pos, termWidth)
}

func PrintSessionHistory(w io.Writer, messages []db.Message, theme UITheme, cfg *config.Config) {
	ui := getUI()
	isGen := false
	collapse := false
	if ui != nil {
		isGen = ui.State.IsGenerating
		collapse = ui.CollapseResults
	}
	hooks := render.HistoryRenderHooks{
		RenderMarkdown:  render.RenderMarkdownContent,
		RenderReasoning: render.RenderReasoningMarkdownContent,
		RenderError:     RenderGenerationError,
	}
	render.PrintSessionHistory(w, messages, theme, cfg, isGen, collapse, hooks)
}

// Internal compatibility wrappers
func extractToolTarget(toolName string, argsJSON string) string {
	return render.ExtractToolTarget(toolName, argsJSON)
}

func renderToolSymbol(toolName string, status toolRenderStatus, theme UITheme) string {
	return render.RenderToolSymbol(toolName, status, theme)
}

func FormatBashCommandLine(symbol string, command string, theme UITheme) string {
	return render.FormatBashCommandLine(symbol, command, theme)
}

func FormatToolTitle(symbol string, toolName string, path string, theme UITheme) string {
	return render.FormatToolTitle(symbol, toolName, path, theme)
}

// NormalizeHistoryInput formats history lines replacing carriage returns and line feeds.
func NormalizeHistoryInput(input string, pos int) (string, int) {
	return render.NormalizeHistoryInput(input, pos)
}

func PrintPromptSeparatorWithSpinner(w io.Writer, showThinking bool, reasoningEffort string, theme UITheme, spinnerFrame string) {
	width, _ := GetTerminalSize()
	fmt.Fprint(w, render.FormatPromptSeparator(showThinking, reasoningEffort, theme, width))
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
	getUI().StateMu.Unlock()

	var buf bytes.Buffer
	buf.WriteString("\x1b[?2026h\x1b7")
	fmt.Fprintf(&buf, "\x1b[%d;1H", height-4-getUI().PasteLinesOffset)

	content := render.FormatStaticStatsContent(spinnerFrame, textToDraw, theme)
	if content != "" {
		fmt.Fprint(&buf, content)
	}

	if spinnerFrame == "" && textToDraw == "" {
		fmt.Fprint(&buf, "\x1b[2K")
	} else {
		fmt.Fprint(&buf, "\x1b[K")
	}
	buf.WriteString("\x1b8\x1b[?2026l")
	_, _ = w.Write(buf.Bytes())
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

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", height-3-getUI().PasteLinesOffset)
	PrintPromptSeparatorWithSpinner(&buf, showThinking, reasoningEffort, theme, spinnerFrame)
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

