package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handleToolsCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	if a == nil || a.Registry == nil {
		fmt.Fprintln(w, "error: agent or tool registry not initialized")
		return
	}

	saveCfg := func() {
		if a.ConfigPath != "" && a.Config != nil {
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}
	}

	syncStatus := func() {
		syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
	}

	openInteractiveMenu := func() {
		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		_ = interactive.RunInteractiveToolsConfig(a, *theme, input, output)
		ui.InitStatusBar(os.Stderr)

		ui.RestoreAfterModal(w, a, kiReader, terminalFromReader(kiReader))
		syncStatus()
	}

	if len(parts) > 1 {
		sub := strings.ToLower(parts[1])
		switch sub {
		case "disable":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /tools disable <tool1> [tool2...]")
				return
			}
			toolsToDisable := parts[2:]
			for _, tName := range toolsToDisable {
				if _, ok := a.Registry.Lookup(tName); !ok {
					fmt.Fprintf(w, "warning: tool '%s' is not currently registered\n", tName)
				}
			}
			a.DisableTools(toolsToDisable...)
			if kiReader != nil && len(kiReader.AllowedTools) > 0 {
				var filtered []string
				for _, at := range kiReader.AllowedTools {
					if !a.IsToolDisabled(at) {
						filtered = append(filtered, at)
					}
				}
				kiReader.AllowedTools = filtered
			}
			saveCfg()
			fmt.Fprintf(w, "disabled tool(s): %s. system prompt updated.\n", strings.Join(toolsToDisable, ", "))
			syncStatus()

		case "enable":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /tools enable <tool1> [tool2...] or /tools enable all")
				return
			}
			if parts[2] == "all" {
				a.EnableAllTools()
				saveCfg()
				fmt.Fprintln(w, "all tools enabled. system prompt updated.")
				syncStatus()
				return
			}
			toolsToEnable := parts[2:]
			a.EnableTools(toolsToEnable...)
			saveCfg()
			fmt.Fprintf(w, "enabled tool(s): %s. system prompt updated.\n", strings.Join(toolsToEnable, ", "))
			syncStatus()

		case "reset":
			a.EnableAllTools()
			saveCfg()
			fmt.Fprintln(w, "all tools reset to enabled. system prompt updated.")
			syncStatus()

		case "toggle":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /tools toggle <tool>")
				return
			}
			toolName := parts[2]
			if a.IsToolDisabled(toolName) {
				a.EnableTools(toolName)
				fmt.Fprintf(w, "tool '%s' is now enabled. system prompt updated.\n", toolName)
			} else {
				a.DisableTools(toolName)
				fmt.Fprintf(w, "tool '%s' is now disabled. system prompt updated.\n", toolName)
			}
			saveCfg()
			syncStatus()

		case "list", "show":
			render.RenderToolsList(w, a, *theme)

		case "menu", "interactive":
			openInteractiveMenu()

		default:
			fmt.Fprintf(w, "unknown subcommand: %s. usage: /tools [list|enable|disable|toggle|reset|menu]\n", sub)
		}
		return
	}

	if kiReader != nil && term.IsTerminal(int(os.Stdin.Fd())) {
		openInteractiveMenu()
		return
	}

	render.RenderToolsList(w, a, *theme)
}