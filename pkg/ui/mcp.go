package ui

import (
	"fmt"
	"io"
	"os"
	"sort"

	"golang.org/x/term"

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

	restartAndSyncMCP := func() {
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		a.StopMCPServers()
		if len(a.Config.MCPServers) > 0 {
			_ = a.StartMCPServers(a.Config.MCPServers)
		}
		syncAgentStatus(a, theme, calcHistoryTokens)
	}

	if len(parts) < 2 {
		input, output, cleanup := getInteractiveIO(kiReader)
		defer cleanup()

		before := config.CopyMCPServers(a.Config.MCPServers)

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveMCPConfig(a.Config, theme, input, output, a.ConfigPath)
		InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			a.ApplyConfig(newConfig)
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}

		var rl *term.Terminal
		if kiReader != nil {
			rl = kiReader.RL
		}
		RestoreAfterModal(w, a, kiReader, rl)

		if errInteractive == nil && newConfig != nil && !config.MCPServersEqual(before, newConfig.MCPServers) {
			a.StopMCPServers()
			if len(a.Config.MCPServers) > 0 {
				_ = a.StartMCPServers(a.Config.MCPServers)
				syncAgentStatus(a, theme, calcHistoryTokens)
			}
		}
		return
	}

	if a.Config.MCPServers == nil {
		a.Config.MCPServers = make(map[string]config.MCPServerConfig)
	}

	sub := parts[1]
	switch sub {
	case "list":
		RenderMCPServers(w, a.Config, theme)
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
		name, url := parts[2], parts[3]
		a.Config.MCPServers[name] = config.MCPServerConfig{
			URL:     url,
			Headers: config.ParseHeaderArgs(parts[4:]),
		}
		fmt.Fprintf(w, "MCP Server '%s' successfully added/updated.\n", name)
		restartAndSyncMCP()
	case "enable", "disable":
		if len(parts) < 3 {
			fmt.Fprintf(w, "usage: /mcp %s <name>\n", sub)
			return
		}
		name := parts[2]
		cfg, ok := a.Config.MCPServers[name]
		if !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}
		cfg.Disabled = (sub == "disable")
		a.Config.MCPServers[name] = cfg
		fmt.Fprintf(w, "MCP Server '%s' successfully %sd.\n", name, sub)
		restartAndSyncMCP()
	case "remove", "delete":
		if len(parts) < 3 {
			fmt.Fprintln(w, "usage: /mcp remove <name>")
			return
		}
		name := parts[2]
		if _, ok := a.Config.MCPServers[name]; !ok {
			fmt.Fprintf(w, "error: MCP server '%s' not found.\n", name)
			return
		}
		delete(a.Config.MCPServers, name)
		fmt.Fprintf(w, "MCP Server '%s' removed.\n", name)
		restartAndSyncMCP()
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