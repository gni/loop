package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleContextCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	pTok, cTok, estimated := calcHistoryTokens()
	latestTurnTokens := 0
	if messages != nil {
		latestTurnTokens = a.GetLatestAssistantCompletionTokens(*messages)
	}
	effLimit := a.GetEffectiveContextLimit(pTok)
	serverLimit := 0
	if d, ok := a.LLMProvider.(agent.ContextLimitDetector); ok {
		serverLimit = d.GetDetectedContextLimit()
	}
	if serverLimit == 0 {
		if p, ok := a.LLMProvider.(*agent.OpenAICompatibleProvider); ok && !p.ThinkingSupportChecked {
			p.ProbeServerCapabilities(context.Background())
			serverLimit = p.GetDetectedContextLimit()
			effLimit = a.GetEffectiveContextLimit(pTok)
		}
	}

	if len(parts) == 1 {
		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		valueStyle := style.NewStyle().Foreground(theme.Text)

		fmt.Fprintln(w, headerStyle.Render("╭───────────────────────────────────────────────────────────────────────────────────────────────────╮"))
		fmt.Fprintln(w, headerStyle.Render("│  CONTEXT WINDOW & ADAPTIVE SIZING CONFIGURATION                                                   │"))
		fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))
		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Context Status"))
		fmt.Fprintf(w, "    Current Prompt Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", pTok)))
		fmt.Fprintf(w, "    Effective Active Tier:  %s tokens\n", valueStyle.Render(fmt.Sprintf("%d", effLimit)))
		fmt.Fprintf(w, "    Configured Ceiling:     %s tokens\n", valueStyle.Render(fmt.Sprintf("%d", a.Config.ContextWindowLimit)))
		if serverLimit > 0 {
			fmt.Fprintf(w, "    Server Detected Limit:  %s tokens\n", valueStyle.Render(fmt.Sprintf("%d", serverLimit)))
		}
		autoStr := "disabled"
		if a.Config.AutoAdaptContext {
			autoStr = "enabled"
		}
		fmt.Fprintf(w, "    Auto-Adaptation:        %s\n", valueStyle.Render(autoStr))
		fmt.Fprintf(w, "    Minimum Window:         %s tokens\n", valueStyle.Render(fmt.Sprintf("%d", a.Config.MinContextWindow)))
		fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))
		fmt.Fprintln(w, "  Commands:")
		fmt.Fprintln(w, "    /context auto            - enable dynamic context tier scaling")
		fmt.Fprintln(w, "    /context off             - disable dynamic tier scaling")
		fmt.Fprintln(w, "    /context <limit>         - set static context limit (e.g. 32768, 65536, 128000)")
		fmt.Fprintln(w, headerStyle.Render("╰───────────────────────────────────────────────────────────────────────────────────────────────────╯"))
		return
	}

	var rl *term.Terminal
	if kiReader != nil {
		rl = kiReader.RL
	}

	arg := strings.ToLower(parts[1])
	if arg == "auto" || arg == "on" || arg == "enable" || arg == "off" || arg == "disable" {
		enable := (arg == "auto" || arg == "on" || arg == "enable")
		a.Config.AutoAdaptContext = enable
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		newLimit := a.GetEffectiveContextLimit(pTok)
		if enable {
			fmt.Fprintf(w, "context auto-adaptation enabled (active tier: %d tokens).\n", newLimit)
		} else {
			fmt.Fprintf(w, "context auto-adaptation disabled (ceiling: %d tokens).\n", newLimit)
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().LastStatusBarText = ""
		ui.GetUI().StateMu.Unlock()
		ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, newLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
		ui.DrawStatusBar(os.Stderr, *theme)
		return
	}

	newLimit, err := strconv.Atoi(parts[1])
	if err != nil || newLimit <= 0 {
		fmt.Fprintf(w, "Invalid context limit value: %v (must be positive integer or 'auto')\n", parts[1])
		return
	}
	a.Config.ContextWindowLimit = newLimit
	_ = config.SaveConfig(a.ConfigPath, a.Config)
	activeLimit := a.GetEffectiveContextLimit(pTok)
	fmt.Fprintf(w, "context window limit set to %d tokens (active limit: %d tokens).\n", newLimit, activeLimit)
	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatusBarText = ""
	ui.GetUI().StateMu.Unlock()
	ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, activeLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
	ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
	ui.DrawStatusBar(os.Stderr, *theme)
}
