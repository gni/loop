package render

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RenderConfig(w io.Writer, cfg *config.Config, theme style.UITheme) {
	termW, _ := terminal.GetDimensions()
	if termW <= 0 {
		termW = 80
	}

	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	keyStyle := style.NewStyle().
		Foreground(theme.Secondary)

	valStyle := style.NewStyle().
		Foreground(theme.Text)

	guidedVal := valStyle.Render("disabled")
	if cfg.GuidedMode {
		guidedVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("enabled")
	}

	approveVal := valStyle.Render("disabled")
	if cfg.AutoApprove {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("auto-approve")
	} else if cfg.ApprovalAlwaysAnswer {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("always")
	}

	askModeVal := valStyle.Render(cfg.AskUserMode)
	if cfg.AskUserMode == "always_ask" {
		askModeVal = style.NewStyle().Foreground(theme.Error).Bold(true).Render("always_ask")
	}

	directVal := valStyle.Render("disabled")
	if cfg.DirectCommands {
		directVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("enabled")
	}

	recapVal := valStyle.Render(fmt.Sprintf("every %d turns", cfg.RecapInterval))
	if cfg.DisableRecap || cfg.RecapInterval <= 0 {
		recapVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("disabled")
	}

	timeoutVal := "disabled"
	if cfg.Timeout > 0 {
		timeoutVal = fmt.Sprintf("%ds", cfg.Timeout)
	}

	disabledToolsVal := "none"
	if len(cfg.DisabledTools) > 0 {
		disabledToolsVal = strings.Join(cfg.DisabledTools, ", ")
	}

	type kvPair struct {
		key string
		val string
	}

	pairs := []kvPair{
		{"active provider:", valStyle.Render(cfg.ActiveProvider)},
		{"temperature:", valStyle.Render(fmt.Sprintf("%.2f", cfg.Temperature))},
		{"guided mode:", guidedVal},
		{"auto-approve:", approveVal},
		{"show thinking:", valStyle.Render(fmt.Sprintf("%v", cfg.ShowThinking))},
		{"collapse results:", valStyle.Render(fmt.Sprintf("%v", cfg.CollapseResults))},
		{"show tokens:", valStyle.Render(fmt.Sprintf("%v", cfg.ShowTokens))},
		{"context limit:", valStyle.Render(fmt.Sprintf("%d tokens", cfg.ContextWindowLimit))},
		{"auto adapt context:", valStyle.Render(fmt.Sprintf("%v", cfg.AutoAdaptContext))},
		{"min context window:", valStyle.Render(fmt.Sprintf("%d tokens", cfg.MinContextWindow))},
		{"max completion tokens:", valStyle.Render(fmt.Sprintf("%d tokens", cfg.MaxCompletionTokens))},
		{"max reasoning steps:", valStyle.Render(fmt.Sprintf("%d", cfg.MaxReasoningSteps))},
		{"direct commands:", directVal},
		{"client cert:", valStyle.Render(cfg.CertFile)},
		{"client key:", valStyle.Render(cfg.KeyFile)},
		{"skip ssl verify:", valStyle.Render(fmt.Sprintf("%v", cfg.SkipVerify))},
		{"stream writes:", valStyle.Render(fmt.Sprintf("%v", cfg.StreamWrites))},
		{"visual theme:", valStyle.Render(cfg.Theme)},
		{"syntax theme:", valStyle.Render(cfg.SyntaxTheme)},
		{"max paste lines:", valStyle.Render(fmt.Sprintf("%d", cfg.MaxPasteLines))},
		{"max paste chars:", valStyle.Render(fmt.Sprintf("%d", cfg.MaxPasteChars))},
		{"llm timeout:", valStyle.Render(timeoutVal)},
		{"approval always:", valStyle.Render(fmt.Sprintf("%v", cfg.ApprovalAlwaysAnswer))},
		{"ask user mode:", askModeVal},
		{"recap interval:", valStyle.Render(fmt.Sprintf("%d", cfg.RecapInterval))},
		{"recap:", recapVal},
		{"prompts file:", valStyle.Render(cfg.PromptsFile)},
		{"disabled tools:", valStyle.Render(disabledToolsVal)},
	}

	formatCell := func(p kvPair) string {
		kPad := 22 - utf8.RuneCountInString(p.key)
		if kPad < 1 {
			kPad = 1
		}
		return fmt.Sprintf("%s%s%s", keyStyle.Render(p.key), strings.Repeat(" ", kPad), p.val)
	}

	var rowsBuilder strings.Builder
	if termW >= 76 {
		rows := (len(pairs) + 1) / 2
		colWidth := (termW - 10) / 2
		if colWidth < 34 {
			colWidth = 34
		}
		sep := style.NewStyle().Foreground(theme.Border).Render(" │ ")

		for r := 0; r < rows; r++ {
			left := formatCell(pairs[r])
			if r+rows < len(pairs) {
				right := formatCell(pairs[r+rows])
				leftVis := utf8.RuneCountInString(style.StripAnsi(left))
				pad := ""
				if leftVis < colWidth {
					pad = strings.Repeat(" ", colWidth-leftVis)
				}
				rowsBuilder.WriteString(fmt.Sprintf("  %s%s%s%s\n", left, pad, sep, right))
			} else {
				rowsBuilder.WriteString(fmt.Sprintf("  %s\n", left))
			}
		}
	} else {
		for _, p := range pairs {
			rowsBuilder.WriteString(fmt.Sprintf("  %s\n", formatCell(p)))
		}
	}

	configStr := fmt.Sprintf(
		"%s\n\n%s\ntip: change any setting via: /config <key> <value> (e.g. /guided on)",
		titleStyle.Render("loop runtime settings"),
		rowsBuilder.String(),
	)

	fmt.Fprintln(w, borderStyle.Render(configStr))
}