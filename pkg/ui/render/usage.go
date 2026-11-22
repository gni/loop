package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/render/markdown"
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
	if m.Usage != nil {
		t.Input += m.Usage.Input
		t.Output += m.Usage.Output
		t.CacheRead += m.Usage.CacheRead
		t.CacheWrite += m.Usage.CacheWrite
		if m.Usage.Cost != nil {
			t.Cost += *m.Usage.Cost
		}
		return
	}
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
			if m.Usage == nil && m.PromptTokens+m.CompletionTokens+m.CacheReadTokens+m.CacheWriteTokens == 0 {
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
		// Cache tokens are already included in normalized input usage.
		totalTok := t.Input + t.Output
		if t.Cost == 0 && totalTok == 0 {
			continue
		}
		out = append(out, UsageCostBreakdownEntry{Key: key, Cost: t.Cost, Tokens: totalTok})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out
}

func RenderStats(w io.Writer, msgs []db.Message, defaultModel string, tr *agent.TimingRecorder, theme style.UITheme) {
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	valueStyle := style.NewStyle().Foreground(theme.Text)

	printUsageHeader(w, theme)

	baseP, baseC := agent.CalculateHistoryTokens(msgs)
	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Base Agent (Main)"))
	fmt.Fprintf(w, "    Prompt Tokens:      %s\n", valueStyle.Render(fmt.Sprintf("%d", baseP)))
	fmt.Fprintf(w, "    Completion Tokens:  %s\n", valueStyle.Render(fmt.Sprintf("%d", baseC)))
	fmt.Fprintf(w, "    Total Cost (Est):   %s\n\n", valueStyle.Render(fmt.Sprintf("%d", baseP+baseC)))

	fmt.Fprintf(w, "  %s:\n", titleStyle.Render("Cost Breakdown By Model"))
	for _, e := range GetUsageCostBreakdown(msgs, defaultModel) {
		key := style.TruncateCells(SanitizeTerminalText(e.Key), 24)
		fmt.Fprintf(w, "    %s cost $%.4f  tokens %d\n", padRightCells(key, 24), e.Cost, e.Tokens)
	}

	if tr != nil {
		fmt.Fprintf(w, "\n  %s:\n", titleStyle.Render("Timings"))
		for _, ns := range []string{"llm", "tool", "compaction"} {
			fmt.Fprintf(w, "    %-10s %dms\n", ns, tr.TotalMs(ns))
		}
	}

	printUsageFooter(w, theme)
}

func usageBoxInnerWidth(w io.Writer) int {
	return max(1, min(markdown.RenderWidth(w)-3, 99))
}

func printUsageHeader(w io.Writer, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	inner := usageBoxInnerWidth(w)
	hline := strings.Repeat("─", inner)
	title := style.TruncateCells("SWARM TOKEN UTILIZATION & COST STATS", max(1, inner-4))
	titleLine := "│  " + title + strings.Repeat(" ", max(0, inner-2-style.CellWidth(title))) + "│"
	fmt.Fprintln(w, headerStyle.Render("╭"+hline+"╮"))
	fmt.Fprintln(w, headerStyle.Render(titleLine))
	fmt.Fprintln(w, headerStyle.Render("├"+hline+"┤"))
}

func printUsageDivider(w io.Writer, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	inner := usageBoxInnerWidth(w)
	fmt.Fprintln(w, headerStyle.Render("├"+strings.Repeat("─", inner)+"┤"))
}

func printUsageFooter(w io.Writer, theme style.UITheme) {
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	inner := usageBoxInnerWidth(w)
	fmt.Fprintln(w, headerStyle.Render("╰"+strings.Repeat("─", inner)+"╯"))
}
