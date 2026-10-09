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

func RenderMCPStartupErrors(w io.Writer, startErrors map[string]error, theme style.UITheme) {
	if len(startErrors) == 0 {
		return
	}
	borderStyle := style.NewStyle().Foreground(theme.Border)
	titleStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	nameStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	msgStyle := style.NewStyle().Foreground(theme.Text)
	hintStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)

	width, _ := terminal.GetDimensions()
	if width <= 0 {
		width = 80
	}
	boxWidth := width - 4
	if boxWidth < 50 {
		boxWidth = 50
	}
	if boxWidth > 100 {
		boxWidth = 100
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", borderStyle.Render("╭"+strings.Repeat("─", boxWidth-2)+"╮"))
	fmt.Fprintf(w, "  %s %-40s %s\n",
		borderStyle.Render("│"),
		titleStyle.Render("mcp server connection warnings:"),
		borderStyle.Render(strings.Repeat(" ", boxWidth-35)+"│"),
	)
	fmt.Fprintf(w, "  %s\n", borderStyle.Render("├"+strings.Repeat("─", boxWidth-2)+"┤"))

	var serverNames []string
	for s := range startErrors {
		serverNames = append(serverNames, s)
	}
	sort.Strings(serverNames)

	for _, name := range serverNames {
		err := startErrors[name]
		errMsg := err.Error()
		if len(errMsg) > boxWidth-25 {
			errMsg = errMsg[:boxWidth-28] + "..."
		}
		fmt.Fprintf(w, "  %s  %s : %s\n",
			borderStyle.Render("│"),
			nameStyle.Render(fmt.Sprintf("%-12s", name)),
			msgStyle.Render(errMsg),
		)
	}

	fmt.Fprintf(w, "  %s\n", borderStyle.Render("├"+strings.Repeat("─", boxWidth-2)+"┤"))
	fmt.Fprintf(w, "  %s  %s\n",
		borderStyle.Render("│"),
		hintStyle.Render("run '/mcp' to check status, or check logs if the server failed to start."),
	)
	fmt.Fprintf(w, "  %s\n", borderStyle.Render("╰"+strings.Repeat("─", boxWidth-2)+"╯"))
	fmt.Fprintln(w)
}

func RenderMCPServers(w io.Writer, cfg *config.Config, theme style.UITheme) {
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
	sb.WriteString(titleStyle.Render("configured mcp servers") + "\n\n")

	if cfg.MCPServers == nil || len(cfg.MCPServers) == 0 {
		sb.WriteString(style.NewStyle().Foreground(theme.Border).Italic(true).Render("  (no mcp servers configured)") + "\n")
	} else {
		var keys []string
		for k := range cfg.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, name := range keys {
			srv := cfg.MCPServers[name]
			var headerParts []string
			for hk, hv := range srv.Headers {
				headerParts = append(headerParts, fmt.Sprintf("%s: %s", hk, hv))
			}
			sort.Strings(headerParts)
			headersDisplay := "none"
			if len(headerParts) > 0 {
				headersDisplay = strings.Join(headerParts, ", ")
			}

			sb.WriteString(fmt.Sprintf("  %-12s : URL: %s | Headers: %s\n",
				style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name),
				srv.URL,
				headersDisplay,
			))
		}
	}

	sb.WriteString("\ntip: manage mcp servers via REPL: /mcp list/add/remove or interactive setup: /mcp")

	fmt.Fprintln(w, borderStyle.Render(sb.String()))
}
