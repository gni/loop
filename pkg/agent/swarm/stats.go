package swarm

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// GetSubagentsCompletionTokens returns the sum of completion tokens across all managed subagents.
func (mam *MultiAgentManager) GetSubagentsCompletionTokens() int {
	if mam == nil {
		return 0
	}
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	total := 0
	for _, ma := range mam.Agents {
		if ma != nil {
			ma.HistoryMu.RLock()
			_, comp := agent.CalculateHistoryTokens(ma.History)
			ma.HistoryMu.RUnlock()
			total += comp
		}
	}
	return total
}

func (mam *MultiAgentManager) RenderStats(w io.Writer, baseMessages []db.Message, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	valueStyle := style.NewStyle().Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("╭───────────────────────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  SWARM TOKEN UTILIZATION & COST STATS                                                             │"))
	fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))

	baseP, baseC := agent.CalculateHistoryTokens(baseMessages)
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
	fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
	fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
	fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", baseP+baseC)))

	totalSwarmP := baseP
	totalSwarmC := baseC

	mam.mu.RLock()
	var names []string
	for name := range mam.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ma := mam.Agents[name]
		ma.HistoryMu.RLock()
		subP, subC := agent.CalculateHistoryTokens(ma.History)
		ma.HistoryMu.RUnlock()

		totalSwarmP += subP
		totalSwarmC += subC

		role := ma.SystemPrompt
		if len(role) > 60 {
			role = role[:57] + "..."
		}
		role = strings.ReplaceAll(role, "\n", " ")

		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Subagent: "+name))
		fmt.Fprintf(w, "    Role/Goal:          %s\n", valueStyle.Render(role))
		fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", subP)))
		fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", subC)))
		fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", subP+subC)))
	}
	mam.mu.RUnlock()

	if len(names) > 0 {
		fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))
		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Total Swarm Utilization"))
		fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", totalSwarmP)))
		fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", totalSwarmC)))
		fmt.Fprintf(w, "    Total Swarm Cost:   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", totalSwarmP+totalSwarmC)))
	}

	fmt.Fprintln(w, headerStyle.Render("╰───────────────────────────────────────────────────────────────────────────────────────────────────╯"))
}
