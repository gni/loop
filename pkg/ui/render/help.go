package render

import (
	"fmt"
	"io"

	"loop/pkg/ui/style"
)

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
		{"/nostop (or /loop)", "configure Eco, Balanced, or Continuous pacing with a required pause"},
		{"/nostop <mode> <seconds>|off|status", "set pacing, disable nonstop, or inspect its settings"},
		{"/guided [on|off|status|approve]", "toggle or inspect guided execution mode (Explore -> Plan -> Execute)"},
		{"/undo", "revert the last conversational turn (user prompt and assistant response)"},
		{"/rollback", "restore files modified in this session to their initial snapshots"},
		{"/tree", "visualize session DAG branches and active conversation path"},
		{"/branch [entry-id]", "switch active branch to a previous entry in the session DAG"},
		{"/fork [entry-id]", "fork current conversation into a new independent session"},
		{"/rename <name>", "rename current conversation session"},
		{"/copy", "copy last assistant response to system clipboard (OSC 52)"},
		{"/export [path]", "export conversation to Markdown or JSONL file"},
		{"/thinking [level]", "view or set reasoning effort (off/low/medium/high/max)"},
		{"/model [name]", "view or set active AI model"},
		{"/new", "start a new conversation session"},
		{"/resume [id]", "select a conversation with ↑/↓ and Enter, or load by id"},
		{"/hotkeys", "display all interactive keyboard shortcuts"},
		{"/config [show|set <k> <v>]", "view or modify runtime settings"},
		{"/session [list|new|load|tree|branch|rename|clear]", "manage persistent conversation sessions"},
		{"/provider", "manage ai endpoints, keys, and model profiles"},
		{"/skills [load <name>]", "list or load reference skills"},
		{"/mcp [enable|disable]", "manage and toggle mcp servers"},
		{"/tools [list|enable|disable|reset]", "manage and toggle tool availability"},
		{"/plugins", "list registered custom tool plugins"},
		{"/extensions", "list custom slash command extensions"},
		{"/reload", "hot-reload plugins and extensions"},
		{"/agent [list|spawn|remove]", "manage multi-agent swarm threads"},
		{"/task [list|view|stream|kill|clear]", "manage background tasks (shorthand: /stop, /kill, /status, /ps)"},
		{"/queue [list|undo|clear]", "view, undo newest, or clear queued prompts"},
		{"/compact (or /compress) [focus]", "compress history to reclaim context tokens (optional focus instructions)"},
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

	RenderHotkeys(w, theme)
}

// RenderHotkeys prints all available terminal keyboard shortcuts.
func RenderHotkeys(w io.Writer, theme style.UITheme) {
	cmdStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	headerStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

	fmt.Fprintln(w)
	fmt.Fprintln(w, headerStyle.Render("keyboard shortcuts:"))
	fmt.Fprintln(w)

	shortcuts := [][]string{
		{"Enter", "send prompt"},
		{"PageUp / PageDown", "scroll chat output when it exceeds the visible area"},
		{"Home / End (browsing)", "oldest output / latest output"},
		{"Mouse wheel", "scroll replies and command output; Shift+drag selects text"},
		{"Esc or typing (browsing)", "return to live output with your draft preserved"},
		{"Alt+Enter", "insert a newline"},
		{"Ctrl+Enter / Shift+Enter", "insert a newline when the terminal reports the modifier"},
		{"Ctrl+N / Ctrl+J", "newline alternatives"},
		{"Ctrl+G", "toggle guided mode on/off"},
		{"Ctrl+C", "interrupt generation / cancel turn"},
		{"Ctrl+Z", "remove newest queued prompt; keep the running turn and draft"},
		{"Ctrl+D", "exit loop cleanly"},
		{"Ctrl+L", "clear screen buffer and redraw"},
		{"Ctrl+O", "toggle collapsible tool outputs"},
		{"Ctrl+R", "cycle reasoning effort (off/low/medium/high/max)"},
		{"Ctrl+T", "toggle streaming thought visibility"},
		{"Tab", "autocomplete slash commands and tool names"},
		{"Up / Down", "move between prompt rows; recall history in a single-line draft"},
		{"Ctrl+P", "recall older prompts; Down at the last row recalls newer prompts"},
	}

	for _, sc := range shortcuts {
		fmt.Fprintf(w, "  %-35s %s\n", cmdStyle.Render(sc[0]), descStyle.Render(sc[1]))
	}
	fmt.Fprintln(w)
}
