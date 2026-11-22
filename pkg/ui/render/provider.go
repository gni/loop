package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/style"
)

// RenderProviders formats and outputs the active and configured AI endpoints.
func RenderProviders(w io.Writer, cfg *config.Config, theme style.UITheme) {
	termW := markdown.RenderWidth(w)
	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	nameStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("configured endpoint providers") + "\n\n")

	if len(cfg.Providers) == 0 {
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
			if p.APIKey != "" {
				apiKeyDisplay = "configured"
			}
			timeoutDisplay := ""
			if p.Timeout > 0 {
				timeoutDisplay = fmt.Sprintf(" | Timeout: %ds", p.Timeout)
			}

			sb.WriteString(fmt.Sprintf("%s%s : URL: %s | Model: %s | API Key: %s%s\n",
				marker,
				padRightCells(nameStyle.Render(SanitizeTerminalText(name)), 12),
				SanitizeTerminalText(p.Endpoint),
				SanitizeTerminalText(p.Model),
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
	sb.WriteString(fmt.Sprintf("\n%s%s : URL: %s | Model: %s%s | (default settings)\n",
		activeMarker,
		padRightCells(nameStyle.Render("default"), 12),
		SanitizeTerminalText(cfg.Endpoint),
		SanitizeTerminalText(cfg.Model),
		defaultTimeoutDisplay,
	))

	sb.WriteString("\ntip: manage providers via REPL: /provider add/select/model/timeout/remove/list")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}
