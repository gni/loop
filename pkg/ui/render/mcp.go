package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/terminal"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/ui/style"
)

func mcpDisplayText(text string) string {
	return strings.Join(strings.Fields(SanitizeTerminalText(text)), " ")
}

func RenderMCPStartupErrors(w io.Writer, startErrors map[string]error, theme style.UITheme) {
	var names []string
	for name, err := range startErrors {
		if err != nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	width, _ := terminal.GetDimensions()
	if width <= 0 {
		width = 80
	}
	width = max(1, width-2)
	title := style.NewStyle().Foreground(theme.Error).Bold(true)
	text := style.NewStyle().Foreground(theme.Text)
	muted := style.NewStyle().Foreground(theme.TextMuted)
	fmt.Fprintln(w)
	fmt.Fprintln(w, title.Render(style.TruncateCells("MCP connection warnings", width)))
	for _, name := range names {
		message := mcpDisplayText(name) + ": " + mcpDisplayText(transporthttp.RedactDiagnostic(startErrors[name].Error()))
		for index, line := range WrapText(message, max(1, width-2)) {
			if index == 3 {
				break
			}
			fmt.Fprintln(w, style.TruncateCells("  "+text.Render(line), width))
		}
	}
	fmt.Fprintln(w, muted.Render(style.TruncateCells("Open /mcp to review or reconnect servers.", width)))
	fmt.Fprintln(w)
}

func RenderMCPServers(w io.Writer, cfg *config.Config, theme style.UITheme) {
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

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("configured mcp servers") + "\n\n")

	if len(cfg.MCPServers) == 0 {
		sb.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no mcp servers configured)") + "\n")
	} else {
		var keys []string
		for k := range cfg.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			srv := cfg.MCPServers[name]
			headersDisplay := "none"
			if len(srv.Headers) > 0 {
				headersDisplay = fmt.Sprintf("%d configured (hidden)", len(srv.Headers))
			}
			displayName := style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(mcpDisplayText(name))

			if srv.Command != "" {
				sb.WriteString(fmt.Sprintf("  %-12s : stdio: %s | Headers: %s\n",
					displayName,
					fmt.Sprintf("%s (%d arguments, %d environment values)", mcpDisplayText(srv.Command), len(srv.Args), len(srv.Env)),
					headersDisplay,
				))
			} else {
				sb.WriteString(fmt.Sprintf("  %-12s : URL: %s | Headers: %s\n",
					displayName,
					mcpDisplayText(transporthttp.RedactURL(srv.URL)),
					headersDisplay,
				))
			}
		}
	}

	sb.WriteString("\ntip: manage mcp servers via REPL: /mcp list/add/remove or interactive setup: /mcp")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}
