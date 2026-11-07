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
		if err := a.ApplyConfig(next); err != nil {
			fmt.Fprintf(w, "MCP update failed: %v\n", err)
			return false
		}
		if err := config.SaveConfig(a.ConfigPath, a.Config); err != nil {
			fmt.Fprintf(w, "MCP settings applied in memory, but could not save: %v\n", err)
			return false
		}
		syncAgentStatus(a, theme, calcHistoryTokens)
		return true
	}

	if len(parts) < 2 {
		input, output, cleanup := getInteractiveIO(kiReader)
		defer cleanup()

		before := config.CopyMCPServers(a.Config.MCPServers)

		ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := RunInteractiveMCPConfig(a.Config, theme, input, output, a.ConfigPath)
		InitStatusBar(os.Stderr)
		if errInteractive != nil {
			fmt.Fprintf(w, "MCP settings menu failed: %v\n", errInteractive)
		} else if newConfig != nil && !config.MCPServersEqual(before, newConfig.MCPServers) {
			applyMCP(newConfig)
		}

		var rl *term.Terminal
		if kiReader != nil {
			rl = kiReader.RL
		}
		RestoreAfterModal(w, a, kiReader, rl)

		return
	}
	next, err := config.Clone(a.Config)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	if next.MCPServers == nil {
		next.MCPServers = make(map[string]config.MCPServerConfig)
	}

	sub := parts[1]
	switch sub {
	case "list":
		render.RenderMCPServers(w, a.Config, theme)
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
				if !cfg.Enabled {
					status = "\x1b[31mdisabled\x1b[0m"
				} else if !active {
					if err, failed := a.MCPStartErrors[name]; failed {
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
		if len(parts) < 3 {
			fmt.Fprintf(w, "usage: /mcp %s <name>\n", sub)
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
		fmt.Fprintf(w, "MCP Server '%s' successfully %sd.\n", name, sub)
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
	fmt.Fprintln(w, "  /mcp list                                  List all configured MCP servers and status")
	fmt.Fprintln(w, "  /mcp tools                                 List all registered MCP tools")
	fmt.Fprintln(w, "  /mcp add <name> <url> [headerKey:val]...   Add or update an MCP server configuration")
	fmt.Fprintln(w, "  /mcp enable <name>                         Enable an MCP server")
	fmt.Fprintln(w, "  /mcp disable <name>                        Disable an MCP server")
	fmt.Fprintln(w, "  /mcp remove <name>                         Remove an MCP server configuration")
}
