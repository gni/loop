package ui

import (
	"fmt"
	"io"
	"os"
	"sort"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
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
	calcHistoryTokens := func() (int, int, bool) {
		var mam *swarm.MultiAgentManager
		if kiReader != nil {
			mam = kiReader.MAM
		}
		return calculateActiveTokenUsage(a, *messages, activeToolAllowlist(kiReader), mam)
	}

	syncStatus := func() {
		syncAgentStatus(a, theme, calcHistoryTokens)
	}

	if len(parts) < 2 {
		input, output, cleanup := getInteractiveIO(kiReader)
		defer cleanup()

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveMCPConfig(a.Config, theme, input, output, a.ConfigPath)
		InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			a.ApplyConfig(newConfig)
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}

		if errInteractive == nil && newConfig != nil {
			// Restart MCP servers with the new config
			a.StopMCPServers()
			if len(a.Config.MCPServers) > 0 {
				_ = a.StartMCPServers(a.Config.MCPServers)
			}
		}

		if kiReader != nil {
			redrawScreen(w, a, kiReader, kiReader.RL)
		} else {
			redrawScreen(w, a, nil, nil)
		}
		return
	}

	sub := parts[1]
	switch sub {
	case "list":
		RenderMCPServers(w, a.Config, theme)

		// Show connection status
		mcpStatuses := a.GetMCPServersStatus()
		if len(a.Config.MCPServers) > 0 {
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("\nmcp connection status:"))
			var keys []string
			for k := range a.Config.MCPServers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, name := range keys {
				status, active := mcpStatuses[name]
				cfg := a.Config.MCPServers[name]

				if cfg.Disabled {
					status = "\x1b[31mdisabled\x1b[0m"
				} else if !active {
					if err, failed := a.McpStartErrors[name]; failed {
						status = fmt.Sprintf("failed to start (%v)", err)
					} else {
						status = "not connected"
					}
				}
				fmt.Fprintf(w, "  - %-10s : %s\n", style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(name), status)
			}
		}
	case "add":
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /mcp add <name> <url> [headerKey:headerVal]...")
			return
		}
		name := parts[2]
		url := parts[3]
		headers := config.ParseHeaderArgs(parts[4:])

		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		a.Config.MCPServers[name] = config.MCPServerConfig{
			URL:     url,
			Headers: headers,
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' successfully added/updated.\n", name)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}

		syncStatus()
	case "enable", "disable":
		if len(parts) < 3 {
			fmt.Fprintf(w, "usage: /mcp %s <name>\n", sub)
			return
		}
		name := parts[2]
		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		cfg, ok := a.Config.MCPServers[name]
		if !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}

		cfg.Disabled = (sub == "disable")
		a.Config.MCPServers[name] = cfg
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' successfully %sd.\n", name, sub)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}

		syncStatus()
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /mcp remove <name>")
			return
		}
		name := parts[2]
		if a.Config.MCPServers == nil {
			a.Config.MCPServers = make(map[string]config.MCPServerConfig)
		}
		if _, ok := a.Config.MCPServers[name]; !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}

		delete(a.Config.MCPServers, name)
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "MCP Server '%s' removed.\n", name)

		// Restart MCP servers
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}

		syncStatus()
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
	fmt.Fprintln(w, "  /mcp list                                  List all configured MCP servers and status")
	fmt.Fprintln(w, "  /mcp tools                                 List all registered MCP tools")
	fmt.Fprintln(w, "  /mcp add <name> <url> [headerKey:val]...   Add or update an MCP server configuration")
	fmt.Fprintln(w, "  /mcp enable <name>                         Enable an MCP server")
	fmt.Fprintln(w, "  /mcp disable <name>                        Disable an MCP server")
	fmt.Fprintln(w, "  /mcp remove <name>                         Remove an MCP server configuration")
}
