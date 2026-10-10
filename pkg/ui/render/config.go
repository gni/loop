package render

import (
	"fmt"
	"io"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// RenderConfig renders the formatted card displaying runtime loop configuration settings.
func RenderConfig(w io.Writer, cfg *config.Config, theme style.UITheme) {
	termW, _ := terminal.GetDimensions()
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

	approveVal := "disabled (interactive mode)"
	if cfg.AutoApprove {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("enabled (auto-approve)")
	} else if cfg.ApprovalAlwaysAnswer {
		approveVal = style.NewStyle().Foreground(theme.Highlight).Bold(true).Render("always answered (unattended)")
	}

	askModeVal := valStyle.Render(cfg.AskUserMode)
	if cfg.AskUserMode == "always_ask" {
		askModeVal = style.NewStyle().Foreground(theme.Error).Bold(true).Render("always_ask (human required)")
	}

	directVal := "disabled"
	if cfg.DirectCommands {
		directVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("enabled")
	}

	recapVal := valStyle.Render(fmt.Sprintf("enabled (inject every %d turns)", cfg.RecapInterval))
	if cfg.DisableRecap || cfg.RecapInterval <= 0 {
		recapVal = style.NewStyle().Foreground(theme.Success).Bold(true).Render("disabled")
	}

	timeoutVal := "disabled (no timeout)"
	if cfg.Timeout > 0 {
		timeoutVal = fmt.Sprintf("%ds", cfg.Timeout)
	}

	configStr := fmt.Sprintf(
		"%s\n\n"+
			"  %-20s %s\n"+
			"  %-20s %.2f\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %d tokens\n"+
			"  %-20s %v\n"+
			"  %-20s %d tokens\n"+
			"  %-20s %d tokens\n"+
			"  %-20s %d\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %v\n"+
			"  %-20s %v\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %d\n"+
			"  %-20s %d\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %s\n"+
			"  %-20s %d\n"+
			"  %-20s %s\n\n"+
			"tip: change any setting via: /config <key> <value> (e.g. /config yes true)",
		titleStyle.Render("loop runtime settings"),
		keyStyle.Render("active provider:"), valStyle.Render(cfg.ActiveProvider),
		keyStyle.Render("temperature:"), cfg.Temperature,
		keyStyle.Render("auto-approve:"), approveVal,
		keyStyle.Render("show thinking:"), cfg.ShowThinking,
		keyStyle.Render("collapse results:"), cfg.CollapseResults,
		keyStyle.Render("show tokens:"), cfg.ShowTokens,
		keyStyle.Render("context limit:"), cfg.ContextWindowLimit,
		keyStyle.Render("auto adapt context:"), cfg.AutoAdaptContext,
		keyStyle.Render("min context window:"), cfg.MinContextWindow,
		keyStyle.Render("max completion tokens:"), cfg.MaxCompletionTokens,
		keyStyle.Render("max reasoning steps:"), cfg.MaxReasoningSteps,
		keyStyle.Render("direct commands:"), directVal,
		keyStyle.Render("client cert:"), valStyle.Render(cfg.CertFile),
		keyStyle.Render("client key:"), valStyle.Render(cfg.KeyFile),
		keyStyle.Render("skip ssl verify:"), valStyle.Render(fmt.Sprintf("%v", cfg.SkipVerify)),
		keyStyle.Render("stream writes:"), cfg.StreamWrites,
		keyStyle.Render("visual theme:"), valStyle.Render(cfg.Theme),
		keyStyle.Render("syntax theme:"), valStyle.Render(cfg.SyntaxTheme),
		keyStyle.Render("max paste lines:"), cfg.MaxPasteLines,
		keyStyle.Render("max paste chars:"), cfg.MaxPasteChars,
		keyStyle.Render("llm timeout:"), valStyle.Render(timeoutVal),
		keyStyle.Render("approval always:"), valStyle.Render(fmt.Sprintf("%v", cfg.ApprovalAlwaysAnswer)),
		keyStyle.Render("ask user mode:"), askModeVal,
		keyStyle.Render("recap interval:"), cfg.RecapInterval,
		keyStyle.Render("recap:"), recapVal,
	)

	fmt.Fprintln(w, borderStyle.Render(configStr))
}
