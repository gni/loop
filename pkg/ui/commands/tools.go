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

	syncStatus := func() {
		if calcHistoryTokens != nil {
			pTok, cTok, estimated := calcHistoryTokens()
			var msgs []db.Message
			if messages != nil {
				msgs = *messages
			}
			latestTurnTokens := a.GetLatestAssistantCompletionTokens(msgs)
			effLimit := a.GetEffectiveContextLimit(pTok)
			ui.GetUI().StateMu.Lock()
			ui.GetUI().LastStatusBarText = ""
			ui.GetUI().StateMu.Unlock()
			ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
			ui.DrawStatusBar(os.Stderr, *theme)
		}
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
			if a.ConfigPath != "" && a.Config != nil {
				_ = config.SaveConfig(a.ConfigPath, a.Config)
			}
			fmt.Fprintf(w, "disabled tool(s): %s. system prompt updated.\n", strings.Join(toolsToDisable, ", "))
			syncStatus()
			return

		case "enable":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /tools enable <tool1> [tool2...] or /tools enable all")
				return
			}
			if parts[2] == "all" {
				a.EnableAllTools()
				if a.ConfigPath != "" && a.Config != nil {
					_ = config.SaveConfig(a.ConfigPath, a.Config)
				}
				fmt.Fprintln(w, "all tools enabled. system prompt updated.")
				syncStatus()
				return
			}
			toolsToEnable := parts[2:]
			a.EnableTools(toolsToEnable...)
			if a.ConfigPath != "" && a.Config != nil {
				_ = config.SaveConfig(a.ConfigPath, a.Config)
			}
			fmt.Fprintf(w, "enabled tool(s): %s. system prompt updated.\n", strings.Join(toolsToEnable, ", "))
			syncStatus()
			return

		case "reset":
			a.EnableAllTools()
			if a.ConfigPath != "" && a.Config != nil {
				_ = config.SaveConfig(a.ConfigPath, a.Config)
			}
			fmt.Fprintln(w, "all tools reset to enabled. system prompt updated.")
			syncStatus()
			return

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
			if a.ConfigPath != "" && a.Config != nil {
				_ = config.SaveConfig(a.ConfigPath, a.Config)
			}
			syncStatus()
			return

		case "list", "show":
			render.RenderToolsList(w, a, *theme)
			return

		case "menu", "interactive":
			input, output, cleanup := interactive.GetInteractiveIO(kiReader)
			defer cleanup()

			ui.ShutdownStatusBar(os.Stderr)
			_ = interactive.RunInteractiveToolsConfig(a, *theme, input, output)
			ui.InitStatusBar(os.Stderr)

			if kiReader != nil {
				ui.RestoreAfterModal(w, a, kiReader, kiReader.RL)
			} else {
				ui.RestoreAfterModal(w, a, nil, nil)
			}
			syncStatus()
			return

		default:
			fmt.Fprintf(w, "unknown subcommand: %s. usage: /tools [list|enable|disable|toggle|reset|menu]\n", sub)
			return
		}
	}

	// Default when no subcommand is given
	fd := int(os.Stdin.Fd())
	if kiReader != nil && term.IsTerminal(fd) {
		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		_ = interactive.RunInteractiveToolsConfig(a, *theme, input, output)
		ui.InitStatusBar(os.Stderr)

		if kiReader != nil {
			ui.RestoreAfterModal(w, a, kiReader, kiReader.RL)
		} else {
			ui.RestoreAfterModal(w, a, nil, nil)
		}
		syncStatus()
		return
	}

	render.RenderToolsList(w, a, *theme)
}
