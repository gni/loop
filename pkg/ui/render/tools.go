package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/ui/style"
)

// RenderToolsList formats and displays the complete tools catalog and their enabled/disabled state.
func RenderToolsList(w io.Writer, a *agent.Agent, theme style.UITheme) {
	if a == nil || a.Registry == nil {
		fmt.Fprintln(w, "no tool registry available.")
		return
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	enabledBadge := style.NewStyle().Foreground(theme.Success).Bold(true).Render("enabled ")
	disabledBadge := style.NewStyle().Foreground(theme.Error).Bold(true).Render("disabled")
	typeStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	hintStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	cmdStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	fmt.Fprintln(w, headerStyle.Render("╭──────────────────────────────────────────────────────────────────────────────────╮"))
	fmt.Fprintln(w, headerStyle.Render("│  TOOLS CATALOG & PERMISSIONS                                                     │"))
	fmt.Fprintln(w, headerStyle.Render("├──────────────────────────────────────────────────────────────────────────────────┤"))

	executors := a.Registry.GetAllExecutors()
	var names []string
	for name := range executors {
		names = append(names, name)
	}
	sort.Strings(names)

	enabledCount := 0
	disabledCount := 0

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
			desc = exec.Definition().Function.Description
			if len(desc) > 65 {
				desc = desc[:62] + "..."
			}
		}

		toolType := classifyTool(name)

		fmt.Fprintf(w, "  %s %-16s [%s] %-10s %s\n",
			icon,
			titleStyle.Render(name),
			statusBadge,
			typeStyle.Render(toolType),
			descStyle.Render(desc),
		)
	}

	fmt.Fprintln(w, headerStyle.Render("├──────────────────────────────────────────────────────────────────────────────────┤"))
	fmt.Fprintf(w, "  summary: %s enabled, %s disabled (total %d)\n\n",
		style.NewStyle().Foreground(theme.Success).Bold(true).Render(fmt.Sprintf("%d", enabledCount)),
		style.NewStyle().Foreground(theme.Error).Bold(true).Render(fmt.Sprintf("%d", disabledCount)),
		len(names),
	)

	fmt.Fprintln(w, headerStyle.Render("slash commands:"))
	fmt.Fprintf(w, "  %-32s %s\n", cmdStyle.Render("/tools disable <name...>"), hintStyle.Render("disable tool(s)"))
	fmt.Fprintf(w, "  %-32s %s\n", cmdStyle.Render("/tools enable <name...>"), hintStyle.Render("enable tool(s)"))
	fmt.Fprintf(w, "  %-32s %s\n", cmdStyle.Render("/tools toggle <name>"), hintStyle.Render("toggle tool enabled/disabled"))
	fmt.Fprintf(w, "  %-32s %s\n", cmdStyle.Render("/tools reset"), hintStyle.Render("enable all tools"))
	fmt.Fprintf(w, "  %-32s %s\n", cmdStyle.Render("/tools menu"), hintStyle.Render("interactive toggle menu"))
	fmt.Fprintln(w, headerStyle.Render("╰──────────────────────────────────────────────────────────────────────────────────╯"))
}

func classifyTool(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		return "mcp"
	}
	if strings.HasPrefix(name, "subagent__") || name == "create_subagent" || name == "remove_subagent" || name == "list_subagents" || name == "audit_subagent" {
		return "subagent"
	}
	switch tool.NormalizeName(name) {
	case "bash", "read", "write", "edit", "grep", "list", "find", "load_skill", "task_status", "task_kill", "todo", "ask_user", "load_mcp":
		return "built-in"
	default:
		return "plugin"
	}
}
