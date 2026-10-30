package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

func RenderSwarmStats(w io.Writer, baseMessages []db.Message, agents []message.AgentUsageSnapshot, tr *agent.TimingRecorder, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	valueStyle := style.NewStyle().Foreground(theme.Text)

	printUsageHeader(w, theme)

	baseP, baseC := agent.CalculateHistoryTokens(baseMessages)
	baseCacheR, baseCacheW := agent.CacheTokenTotals(baseMessages)
	allMessages := append([]db.Message(nil), baseMessages...)
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
	fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
	fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
	if baseCacheR > 0 || baseCacheW > 0 {
		fmt.Fprintf(w, "    Cache Read/Write:   %s\n", valueStyle.Render(fmt.Sprintf("%d / %d", baseCacheR, baseCacheW)))
	}
	fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(formatCost(message.TotalUsage(baseMessages).Cost)))

	totalSwarmP := baseP
	totalSwarmC := baseC

	if len(agents) > 0 {
		for _, snapshot := range agents {
			name := snapshot.Name
			subP, subC := agent.CalculateHistoryTokens(snapshot.Messages)
			subUsage := message.TotalUsage(snapshot.Messages)
			allMessages = append(allMessages, snapshot.Messages...)
			totalSwarmP += subP
			totalSwarmC += subC

			role := strings.ReplaceAll(snapshot.Role, "\n", " ")
			if len(role) > 60 {
				role = role[:57] + "..."
			}

			fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Subagent: "+name))
			fmt.Fprintf(w, "    Role/Goal:          %s\n", valueStyle.Render(role))
			fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", subP)))
			fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", subC)))
			fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(formatCost(subUsage.Cost)))
		}

		fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))
		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Total Swarm Utilization"))
		fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", totalSwarmP)))
		fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", totalSwarmC)))
		fmt.Fprintf(w, "    Total Swarm Cost:   %s\n\n", valueStyle.Render(formatCost(message.TotalUsage(allMessages).Cost)))
	}

	if tr != nil {
		fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Timings"))
		for _, ns := range []string{"llm", "tool", "compaction"} {
			fmt.Fprintf(w, "    %-10s %dms\n", ns, tr.TotalMs(ns))
		}
	}

	printUsageFooter(w, theme)
}

func formatCost(cost *float64) string {
	if cost == nil {
		return "unknown"
	}
	return fmt.Sprintf("$%.6f", *cost)
}
