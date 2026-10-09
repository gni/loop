package commands

import (
	"fmt"
	"io"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func handleStatsCommand(a *agent.Agent, messages *[]db.Message, theme *style.UITheme, w io.Writer) {
	if mam, ok := a.MultiAgentManager.(*swarm.MultiAgentManager); ok && mam != nil {
		mam.RenderStats(w, *messages, *theme)
	} else {
		// Fallback: print only base agent stats if MultiAgentManager is nil
		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		valueStyle := style.NewStyle().Foreground(theme.Text)

		fmt.Fprintln(w, headerStyle.Render("╭───────────────────────────────────────────────────────────────────────────────────────────────────╮"))
		fmt.Fprintln(w, headerStyle.Render("│  SWARM TOKEN UTILIZATION & COST STATS                                                             │"))
		fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))

		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		baseP, baseC := agent.CalculateHistoryTokens(msgs)
		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
		fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
		fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
		fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", baseP+baseC)))
		fmt.Fprintln(w, headerStyle.Render("╰───────────────────────────────────────────────────────────────────────────────────────────────────╯"))
	}
}
