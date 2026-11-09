package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func HandleMCPCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme UITheme,
	w io.Writer,
	kiReader *keyInterceptorReader,
) {
	if a == nil || a.Config == nil {
		fmt.Fprintln(w, "MCP configuration is unavailable")
		return
	}
	calcHistoryTokens := func() (int, int, bool) {
		var mam *swarm.MultiAgentManager
		if kiReader != nil {
			mam = kiReader.MAM
		}
		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		return calculateActiveTokenUsage(a, msgs, activeToolAllowlist(kiReader), mam)
	}

	applyMCP := func(next *config.Config) bool {
		applyErr := a.UpdateConfig(next)
		if !agent.ConfigApplied(applyErr) {
			fmt.Fprintf(w, "MCP update failed: %v\n", applyErr)
			return false
		}
		if applyErr != nil {
			fmt.Fprintln(w, applyErr)
		}
		syncAgentStatus(a, theme, calcHistoryTokens)
		if err := a.SaveConfig(); err != nil {
			fmt.Fprintf(w, "MCP settings applied in memory, but could not save: %v\n", err)
			return false
		}
		return true
	}

	if len(parts) < 2 {
		snapshot, snapshotErr := a.Configuration()
		if snapshotErr != nil {
			fmt.Fprintln(w, snapshotErr)
			return
		}
		input, output, cleanup := getInteractiveIO(kiReader)
		defer cleanup()

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveMCPConfig(snapshot, theme, input, output, interactive.MCPConfigActions{Reconnect: func(next *config.Config) error {
			changed := a.Config.MCP != next.MCP || !config.MCPServersEqual(a.Config.MCPServers, next.MCPServers)
			var applyErr error
			if changed {
				applyErr = a.UpdateConfig(next)
				if !agent.ConfigApplied(applyErr) {
					return applyErr
				}
			}
			reconnectErr := a.ReconnectMCPServers()
			syncAgentStatus(a, theme, calcHistoryTokens)
			if saveErr := a.SaveConfig(); saveErr != nil {
				return errors.Join(applyErr, reconnectErr, fmt.Errorf("MCP settings applied, but could not save: %w", saveErr))
			}
			return errors.Join(applyErr, reconnectErr)
		}, Statuses: a.MCPServerStatuses, Configuration: a.Configuration})
		InitStatusBar(os.Stderr)
		if errInteractive != nil {
			fmt.Fprintf(w, "MCP settings menu failed: %v\n", errInteractive)
		} else if newConfig != nil && (a.Config.MCP != newConfig.MCP || !config.MCPServersEqual(a.Config.MCPServers, newConfig.MCPServers)) {
			applyMCP(newConfig)
		}

		var rl *term.Terminal
		if kiReader != nil {
			rl = kiReader.RL
		}
		RestoreAfterModal(w, a, kiReader, rl)

		return
	}
	next, err := a.Configuration()
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	if next.MCPServers == nil {
		next.MCPServers = make(map[string]config.MCPServerConfig)
	}

	sub := parts[1]
	switch sub {
	case "list", "status":
		state := "disabled"
		if next.MCP {
			state = "enabled"
		}
		fmt.Fprintf(w, "MCP: %s\n", state)
		render.RenderMCPServers(w, next, theme)
		mcpStatuses := a.GetMCPServersStatus()
		if len(next.MCPServers) > 0 {
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("\nmcp connection status:"))
			keys := make([]string, 0, len(next.MCPServers))
			for k := range next.MCPServers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, name := range keys {
				status, active := mcpStatuses[name]
				cfg := next.MCPServers[name]
				if !next.MCP {
					status = "MCP disabled"
				} else if !cfg.Enabled {
					status = "\x1b[31mdisabled\x1b[0m"
				} else if !active {
					status = "not connected"
				}
				name = strings.Join(strings.Fields(render.SanitizeTerminalText(name)), " ")
				status = strings.Join(strings.Fields(render.SanitizeTerminalText(status)), " ")
				fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
			}
		}
	case "add":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /mcp add <name> <url> [headerKey:headerVal]...")
			return
		}
		name, url := parts[2], parts[3]
		next.MCPServers[name] = config.MCPServerConfig{
			Enabled: true,
			URL:     url,
			Headers: config.ParseHeaderArgs(parts[4:]),
		}
		if !applyMCP(next) {
			return
		}
		fmt.Fprintf(w, "MCP Server '%s' successfully added/updated.\n", name)
	case "enable", "disable":
		if len(parts) > 3 {
			fmt.Fprintf(w, "usage: /mcp %s [name]\n", sub)
			return
		}
		if len(parts) == 2 {
			if err := next.SetValue("mcp", fmt.Sprint(sub == "enable")); err != nil {
				fmt.Fprintln(w, err)
				return
			}
			if applyMCP(next) {
				fmt.Fprintf(w, "MCP %sd. Server settings kept.\n", sub)
			}
			return
		}
		name := parts[2]
		cfg, ok := next.MCPServers[name]
		if !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}
		cfg.Enabled = (sub == "enable")
		next.MCPServers[name] = cfg
		if !applyMCP(next) {
			return
		}
		fmt.Fprintf(w, "MCP Server '%s' %sd.\n", name, sub)
		if cfg.Enabled && !a.Config.MCP {
			fmt.Fprintln(w, "MCP is disabled globally. Use /mcp enable to activate enabled servers.")
		}
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /mcp remove <name>")
			return
		}
		name := parts[2]
		if _, ok := next.MCPServers[name]; !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}
		delete(next.MCPServers, name)
		if !applyMCP(next) {
			return
		}
		fmt.Fprintf(w, "MCP Server '%s' removed.\n", name)
	case "tools":
		mcpTools := a.GetMCPTools()
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("available mcp tools:"))
		if len(mcpTools) == 0 {
			fmt.Fprintln(w, "  (no tools registered)")
		} else {
			for _, t := range mcpTools {
				fmt.Fprintf(w, "  - %s: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(t.Function.Name),
					t.Function.Description,
				)
			}
		}
	default:
		fmt.Fprintf(w, "unknown subcommand '%s'.\n", sub)
		printMCPHelp(w, theme)
	}
}

func printMCPHelp(w io.Writer, theme UITheme) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Usage:"))
	fmt.Fprintln(w, "  /mcp                                       Open the interactive MCP editor")
	fmt.Fprintln(w, "  /mcp list                                  Show MCP status and configured servers")
	fmt.Fprintln(w, "  /mcp tools                                 List all registered MCP tools")
	fmt.Fprintln(w, "  /mcp add <name> <url> [headerKey:val]...   Add or update an MCP server configuration")
	fmt.Fprintln(w, "  /mcp enable [name]                         Enable MCP, or one named server")
	fmt.Fprintln(w, "  /mcp disable [name]                        Disable MCP, or one named server")
	fmt.Fprintln(w, "  /mcp remove <name>                         Remove an MCP server configuration")
}
