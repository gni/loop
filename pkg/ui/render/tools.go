package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/style"
)

// RenderToolsList formats and displays the complete tools catalog and their enabled/disabled state.
func RenderToolsList(w io.Writer, a *agent.Agent, theme style.UITheme) {
	if a == nil || a.Registry == nil {
		fmt.Fprintln(w, "no tool registry available.")
		return
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Text).Bold(true)
	enabledBadge := style.NewStyle().Foreground(theme.Success).Render("enabled ")
	disabledBadge := style.NewStyle().Foreground(theme.Error).Render("disabled")
	typeStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)
	descStyle := style.NewStyle().Foreground(theme.TextMuted)
	hintStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)
	cmdStyle := style.NewStyle().Foreground(theme.Primary)

	termW := markdown.RenderWidth(w)
	inner := max(1, min(termW-3, 82))
	hline := strings.Repeat("─", inner)
	titleText := style.TruncateCells("TOOLS CATALOG & PERMISSIONS", max(1, inner-4))
	vbar := borderStyle.Render("│")
	titleLine := vbar + "  " + headerStyle.Render(titleText) + strings.Repeat(" ", max(0, inner-2-style.CellWidth(titleText))) + vbar

	fmt.Fprintln(w, borderStyle.Render("╭"+hline+"╮"))
	fmt.Fprintln(w, titleLine)
	fmt.Fprintln(w, borderStyle.Render("├"+hline+"┤"))

	executors := a.Registry.GetAllExecutors()
	var names []string
	for name := range executors {
		names = append(names, name)
	}
	sort.Strings(names)

	enabledCount := 0
	disabledCount := 0
	descBudget := max(12, min(65, termW-46))

	for _, name := range names {
		exec := executors[name]
		disabled := a.IsToolDisabled(name)
		statusBadge := enabledBadge
		icon := style.NewStyle().Foreground(theme.Success).Render("✓")
		if disabled {
			statusBadge = disabledBadge
			icon = style.NewStyle().Foreground(theme.Error).Render("✗")
			disabledCount++
		} else {
			enabledCount++
		}

		desc := ""
		if exec != nil {
			desc = strings.Join(strings.Fields(SanitizeTerminalText(exec.Definition().Function.Description)), " ")
			desc = style.TruncateCells(desc, descBudget)
		}

		toolType := classifyTool(name)

		fmt.Fprintf(w, "  %s %s [%s] %s %s\n",
			icon,
			padRightCells(titleStyle.Render(SanitizeTerminalText(name)), 16),
			statusBadge,
			padRightCells(typeStyle.Render(toolType), 10),
			descStyle.Render(desc),
		)
	}

	fmt.Fprintln(w, borderStyle.Render("├"+hline+"┤"))
	fmt.Fprintf(w, "  summary: %s enabled, %s disabled (total %d)\n\n",
		style.NewStyle().Foreground(theme.Success).Bold(true).Render(fmt.Sprintf("%d", enabledCount)),
		style.NewStyle().Foreground(theme.Error).Bold(true).Render(fmt.Sprintf("%d", disabledCount)),
		len(names),
	)

	fmt.Fprintln(w, headerStyle.Render("slash commands:"))
	fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render("/tools disable <name...>"), 32), hintStyle.Render("disable tool(s)"))
	fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render("/tools enable <name...>"), 32), hintStyle.Render("enable tool(s)"))
	fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render("/tools toggle <name>"), 32), hintStyle.Render("toggle tool enabled/disabled"))
	fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render("/tools reset"), 32), hintStyle.Render("enable all tools"))
	fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render("/tools menu"), 32), hintStyle.Render("interactive toggle menu"))
	fmt.Fprintln(w, borderStyle.Render("╰"+hline+"╯"))
}

func classifyTool(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		return "mcp"
	}
	if tool.IsSubagentTool(name) {
		return "subagent"
	}
	if domaintool.IsBuiltinName(name) {
		return "built-in"
	}
	return "plugin"
}
