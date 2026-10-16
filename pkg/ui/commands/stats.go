package commands

import (
	"fmt"
	"io"
	"sort"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

type UsageTotals struct {
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Cost       float64 `json:"cost"`
}

var CostPerMillionTokens = map[string]struct{ In, Out float64 }{}

func AddUsageToTotals(t *UsageTotals, m message.Message) {
	t.Input += m.PromptTokens
	t.Output += m.CompletionTokens
	t.CacheRead += m.CacheReadTokens
	t.CacheWrite += m.CacheWriteTokens
	price, ok := CostPerMillionTokens[m.Name]
	if !ok {
		price = CostPerMillionTokens["default"]
	}
	t.Cost += (float64(m.PromptTokens)*price.In + float64(m.CompletionTokens)*price.Out) / 1e6
}

type UsageCostBreakdownEntry struct {
	Key    string
	Cost   float64
	Tokens int
}

func GetUsageCostBreakdown(messages []db.Message, defaultModel string) []UsageCostBreakdownEntry {
	totals := map[string]*UsageTotals{}
	for _, m := range messages {
		key := ""
		switch m.Role {
		case "assistant":
			key = m.Name
			if key == "" {
				key = defaultModel
			}
			if key == "" {
				key = "unknown"
			}
		case "toolResult", "system":
			if m.PromptTokens+m.CompletionTokens+m.CacheReadTokens+m.CacheWriteTokens == 0 {
				continue
			}
			key = "Tools/summaries"
		default:
			continue
		}
		t := totals[key]
		if t == nil {
			t = &UsageTotals{}
			totals[key] = t
		}
		AddUsageToTotals(t, m)
	}

	out := make([]UsageCostBreakdownEntry, 0, len(totals))
	for key, t := range totals {
		totalTok := t.Input + t.Output + t.CacheRead + t.CacheWrite
		if t.Cost == 0 && totalTok == 0 {
			continue
		}
		out = append(out, UsageCostBreakdownEntry{Key: key, Cost: t.Cost, Tokens: totalTok})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out
}

func handleStatsCommand(a *agent.Agent, messages *[]db.Message, theme *style.UITheme, w io.Writer) {
	var msgs []db.Message
	if messages != nil {
		msgs = *messages
	}

	if mam, ok := a.MultiAgentManager.(*swarm.MultiAgentManager); ok && mam != nil {
		mam.RenderStats(w, msgs, *theme)
		return
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	valueStyle := style.NewStyle().Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("╭───────────────────────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  SWARM TOKEN UTILIZATION & COST STATS                                                             │"))
	fmt.Fprintln(w, headerStyle.Render("├───────────────────────────────────────────────────────────────────────────────────────────────────┤"))

	baseP, baseC := agent.CalculateHistoryTokens(msgs)
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
	fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
	fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
	fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", baseP+baseC)))

	defaultModel := ""
	if a != nil && a.Config != nil {
		defaultModel = a.Config.Model
	}
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Cost Breakdown By Model"))
	for _, e := range GetUsageCostBreakdown(msgs, defaultModel) {
		fmt.Fprintf(w, "    %-24s cost $%.4f  tokens %d\n", e.Key, e.Cost, e.Tokens)
	}

	if tr := agent.GetTimingRecorder(); tr != nil {
		fmt.Fprintf(w, "\n  %s:\n", titleStyle.Render("Timings"))
		for _, ns := range []string{"llm", "tool", "compaction"} {
			fmt.Fprintf(w, "    %-10s %dms\n", ns, tr.TotalMs(ns))
		}
	}

	fmt.Fprintln(w, headerStyle.Render("╰───────────────────────────────────────────────────────────────────────────────────────────────────╯"))
}