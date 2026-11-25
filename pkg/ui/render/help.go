package render

import (
	"fmt"
	"io"

	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/style"
)

func RenderHelp(w io.Writer, theme style.UITheme) {
	headerStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Underline(true)

	cmdStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	descStyle := style.NewStyle().
		Foreground(theme.Text)

	fmt.Fprintln(w, headerStyle.Render("slash commands reference:"))
	fmt.Fprintln(w)

	commands := [][]string{
		{"/find <text> | next | prev | clear", "search this conversation, including older history"},
		{"/latest", "return to the latest output"},
		{"/changes [diff]", "review recorded file edits and Git working-tree changes"},
		{"/doctor [offline]", "check settings, provider connection and local tools"},
		{"/config logo false", "hide the logo; use true to show it"},
		{"/config reduced_motion true", "keep the logo still"},
		{"/config logo_brightness <10-100>", "adjust logo contrast"},
		{"/nostop (or /loop)", "configure Eco, Balanced, or Continuous pacing with a required pause"},
		{"/nostop <mode> <seconds>|off|status", "set pacing, disable nonstop, or inspect its settings"},
		{"/guided [on|off|status|approve]", "toggle or inspect guided execution mode (Explore -> Plan -> Execute)"},
		{"/undo", "revert the last conversational turn (user prompt and assistant response)"},
		{"/rollback", "restore files modified in this session to their initial snapshots"},
		{"/tree", "visualize session DAG branches and active conversation path"},
		{"/branch [entry-id]", "switch active branch to a previous entry in the session DAG"},
		{"/fork [entry-id]", "fork current conversation into a new independent session"},
		{"/rename <name>", "rename current conversation session"},
		{"/copy [code|<n>|list]", "copy last assistant reply or fenced code block to clipboard"},
		{"/config native_scroll true", "use native terminal scrollback without mouse capture (default: false)"},
		{"/config bash_syntax_color false", "disable syntax highlighting on bash commands and results (default: true)"},
		{"/export [path]", "export conversation to Markdown or JSONL file"},
		{"/thinking [level]", "view or set reasoning effort (off/low/medium/high/max)"},
		{"/model [name]", "view or set active AI model"},
		{"/new", "start a new conversation session"},
		{"/resume [id]", "filter conversations by typing, then choose with ↑/↓ and Enter"},
		{"/hotkeys", "display all interactive keyboard shortcuts"},
		{"/config [show|set <k> <v>]", "everyday settings, with an advanced-settings toggle"},
		{"/theme [name|create|edit|delete|reload|list]", "pick, create, or edit UI color themes (~/.loop/themes/*.json)"},
		{"/session [list|new|load|tree|branch|rename|clear]", "manage persistent conversation sessions"},
		{"/provider", "manage ai endpoints, keys, and model profiles"},
		{"/skills [list|load [name]|reload]", "filter and pick a skill, list skills, or load by name"},
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

	colWidth := min(35, max(16, markdown.RenderWidth(w)/2))
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render(cmd[0]), colWidth), descStyle.Render(cmd[1]))
	}
	fmt.Fprintln(w)

	RenderHotkeys(w, theme)
}

// RenderHotkeys prints all available terminal keyboard shortcuts.
func RenderHotkeys(w io.Writer, theme style.UITheme) {
	cmdStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true).Underline(true)

	fmt.Fprintln(w)
	fmt.Fprintln(w, headerStyle.Render("keyboard shortcuts:"))
	fmt.Fprintln(w)

	shortcuts := [][]string{
		{"Enter", "send prompt"},
		{"Ctrl+F", "search conversation; Enter finds, Esc cancels"},
		{"Ctrl+N / Ctrl+P (search active)", "next / previous search match"},
		{"Mouse wheel / click", "scroll chat history and click prompt/status buttons (default)"},
		{"PageUp / PageDown", "scroll chat output in paged history mode"},
		{"Home / End (browsing)", "oldest output / latest output"},
		{"Esc (browsing)", "return to live output with your draft preserved"},
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
		{"Ctrl+Y", "cycle UI color theme"},
		{"Tab", "autocomplete slash commands and tool names"},
		{"Up / Down", "move between prompt rows; recall history in a single-line draft"},
		{"Ctrl+P", "recall older prompts; Down at the last row recalls newer prompts"},
	}

	colWidth := min(35, max(16, markdown.RenderWidth(w)/2))
	for _, sc := range shortcuts {
		fmt.Fprintf(w, "  %s %s\n", padRightCells(cmdStyle.Render(sc[0]), colWidth), descStyle.Render(sc[1]))
	}
	fmt.Fprintln(w)
}
