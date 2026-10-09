package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// RenderProviders formats and outputs the active and configured AI endpoints.
func RenderProviders(w io.Writer, cfg *config.Config, theme style.UITheme) {
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

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("configured endpoint providers") + "\n\n")

	if cfg.Providers == nil || len(cfg.Providers) == 0 {
		sb.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no endpoint providers configured)") + "\n")
	} else {
		var keys []string
		for k := range cfg.Providers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			p := cfg.Providers[name]
			marker := "  "
			if name == cfg.ActiveProvider {
				marker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
			}

			apiKeyDisplay := "none"
			if p.ApiKey != "" {
				apiKeyDisplay = "configured"
			}
			timeoutDisplay := ""
			if p.Timeout > 0 {
				timeoutDisplay = fmt.Sprintf(" | Timeout: %ds", p.Timeout)
			}

			sb.WriteString(fmt.Sprintf("%s%-12s : URL: %s | Model: %s | API Key: %s%s\n",
				marker,
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
				p.Endpoint,
				p.Model,
				apiKeyDisplay,
				timeoutDisplay,
			))
		}
	}

	activeMarker := "  "
	if cfg.ActiveProvider == "" {
		activeMarker = style.NewStyle().Foreground(theme.Success).Render("➔ ")
	}
	defaultTimeoutDisplay := ""
	if cfg.Timeout > 0 {
		defaultTimeoutDisplay = fmt.Sprintf(" | Timeout: %ds", cfg.Timeout)
	}
	sb.WriteString(fmt.Sprintf("\n%s%-12s : URL: %s | Model: %s%s | (default settings)\n",
		activeMarker,
		style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("default"),
		cfg.Endpoint,
		cfg.Model,
		defaultTimeoutDisplay,
	))

	sb.WriteString("\ntip: manage providers via REPL: /provider add/select/model/timeout/remove/list")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}
