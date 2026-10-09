package render

import (
	"fmt"
	"io"

	"loop/pkg/ui/style"
)

// RenderHelp formats and displays the slash command reference and keyboard shortcut guide.
func RenderHelp(w io.Writer, theme style.UITheme) {
	headerStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Underline(true)

	cmdStyle := style.NewStyle().
		Foreground(theme.Secondary).
		Bold(true)

	descStyle := style.NewStyle().
		Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("slash commands reference:"))
	fmt.Fprintln(w)

	commands := [][]string{
		{"/config [show|set <k> <v>]", "view or modify runtime settings"},
		{"/session [list|new|load|clear]", "manage persistent conversation sessions"},
		{"/provider", "manage ai endpoints, keys, and model profiles"},
		{"/skills [load <name>]", "list or load reference skills"},
		{"/mcp [enable|disable]", "manage and toggle mcp servers"},
		{"/plugins", "list registered custom tool plugins"},
		{"/extensions", "list custom slash command extensions"},
		{"/reload", "hot-reload plugins and extensions"},
		{"/agent [list|spawn|remove]", "manage multi-agent swarm threads"},
		{"/task [list|view|stream|kill]", "manage background tasks"},
		{"/queue [list|clear]", "view or clear queued prompts"},
		{"/compress", "compress history to reclaim context tokens"},
		{"/context [auto|off|<limit>]", "view or configure adaptive context window scaling"},
		{"/tokens", "display token utilization and cost stats across swarm"},
		{"/debug", "view path and status of debug execution log"},
		{"/clear", "clear conversation history and start fresh"},
		{"/help", "display this help menu"},
		{"/exit", "exit the loop application"},
	}

	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render(cmd[0]), descStyle.Render(cmd[1]))
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, headerStyle.Render("keyboard shortcuts:"))
	fmt.Fprintln(w)

	shortcuts := [][]string{
		{"Enter", "submit prompt to agent"},
		{"Ctrl+C", "interrupt generation / cancel turn"},
		{"Ctrl+D", "exit loop cleanly"},
		{"Ctrl+L", "clear screen buffer and redraw"},
		{"Ctrl+O", "toggle collapsible tool outputs"},
		{"Ctrl+R", "reverse search conversation history"},
		{"Tab", "autocomplete slash commands and tool names"},
		{"Up / Down", "navigate prompt history"},
	}

	for _, sc := range shortcuts {
		fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render(sc[0]), descStyle.Render(sc[1]))
	}
	fmt.Fprintln(w)
}
