package render

import (
	"fmt"
	"strings"

	"loop/pkg/ui/style"
)

// SlashCommandItem describes a selectable entry in the live slash-command menu.
type SlashCommandItem struct {
	Name        string
	Args        string
	Description string
	InsertText  string
	TakesArgs   bool
	HasSubs     bool
}

// AcceptText returns the prompt text applied when accepting the item via Enter or click.
func (item SlashCommandItem) AcceptText() string {
	if item.InsertText != "" {
		return item.InsertText
	}
	if item.TakesArgs {
		if strings.HasSuffix(item.Name, " ") {
			return item.Name
		}
		return item.Name + " "
	}
	return strings.TrimRight(item.Name, " ")
}

// TabText returns the prompt text applied when pressing Tab to complete or drill into subcommands.
func (item SlashCommandItem) TabText() string {
	if item.InsertText != "" {
		return item.InsertText
	}
	if item.TakesArgs || item.HasSubs {
		if strings.HasSuffix(item.Name, " ") {
			return item.Name
		}
		return item.Name + " "
	}
	return strings.TrimRight(item.Name, " ")
}

var builtinSlashCommands = []SlashCommandItem{
	{Name: "/config", Description: "everyday settings and preferences", HasSubs: true},
	{Name: "/skills", Description: "list or load reference skills", HasSubs: true},
	{Name: "/provider", Description: "manage AI endpoints, keys, and models", HasSubs: true},
	{Name: "/model", Args: "[name]", Description: "view or set active AI model"},
	{Name: "/session", Description: "manage persistent conversation sessions", HasSubs: true},
	{Name: "/resume", Args: "[id]", Description: "filter and resume a previous conversation"},
	{Name: "/new", Description: "start a new conversation session"},
	{Name: "/clear", Description: "clear conversation history and start fresh"},
	{Name: "/compact", Args: "[focus]", Description: "compress history to reclaim context tokens"},
	{Name: "/context", Args: "[auto|off]", Description: "view or configure context window limit", HasSubs: true},
	{Name: "/tools", Description: "manage and toggle available tools", HasSubs: true},
	{Name: "/mcp", Description: "manage and toggle MCP servers"},
	{Name: "/thinking", Args: "[level]", Description: "view or set reasoning effort", HasSubs: true},
	{Name: "/guided", Args: "[on|off]", Description: "toggle guided mode (Explore -> Plan -> Execute)", HasSubs: true},
	{Name: "/plan", Description: "view or create an execution plan"},
	{Name: "/nostop", Args: "[mode]", Description: "configure Eco, Balanced, or Continuous pacing", HasSubs: true},
	{Name: "/agent", Description: "manage multi-agent swarm threads", HasSubs: true},
	{Name: "/task", Description: "manage background tasks", HasSubs: true},
	{Name: "/changes", Args: "[diff]", Description: "review recorded file edits and git changes", HasSubs: true},
	{Name: "/undo", Description: "revert the last conversational turn"},
	{Name: "/rollback", Description: "restore modified files to session snapshots"},
	{Name: "/tree", Description: "visualize session branches and active path"},
	{Name: "/branch", Args: "[id]", Description: "switch active branch in session history"},
	{Name: "/fork", Args: "[id]", Description: "fork conversation into a new session"},
	{Name: "/rename", Args: "<name>", Description: "rename current conversation session", TakesArgs: true},
	{Name: "/find", Args: "<text>", Description: "search conversation including older history", TakesArgs: true, HasSubs: true},
	{Name: "/latest", Description: "return to the latest output"},
	{Name: "/copy", Description: "copy last assistant response to clipboard"},
	{Name: "/export", Args: "[path]", Description: "export conversation to Markdown or JSONL"},
	{Name: "/tokens", Description: "display token utilization and cost stats"},
	{Name: "/queue", Args: "[list|undo|clear]", Description: "view, undo, or clear queued prompts", HasSubs: true},
	{Name: "/toggle", Description: "toggle collapsed or expanded tool outputs"},
	{Name: "/doctor", Args: "[offline]", Description: "check settings, provider connection, and tools", HasSubs: true},
	{Name: "/plugins", Description: "list registered custom tool plugins"},
	{Name: "/extensions", Description: "list custom slash command extensions"},
	{Name: "/reload", Description: "hot-reload plugins and extensions"},
	{Name: "/debug", Description: "view path and status of debug log"},
	{Name: "/hotkeys", Description: "display interactive keyboard shortcuts"},
	{Name: "/help", Description: "display full slash commands reference"},
	{Name: "/exit", Description: "exit loop cleanly"},
}

// BuiltinSlashCommands returns a copy of the curated built-in top-level slash commands.
func BuiltinSlashCommands() []SlashCommandItem {
	out := make([]SlashCommandItem, len(builtinSlashCommands))
	copy(out, builtinSlashCommands)
	return out
}

func builtinSubcommands(cmd string) []SlashCommandItem {
	switch cmd {
	case "/config":
		return []SlashCommandItem{
			{Name: "/config", Description: "open interactive settings menu"},
			{Name: "/config show", Description: "print all current configuration settings"},
			{Name: "/config set", Args: "<key> <val>", Description: "set a configuration key and value", TakesArgs: true, HasSubs: true},
		}
	case "/skills", "/skill":
		return []SlashCommandItem{
			{Name: "/skills", Description: "list available skills"},
			{Name: "/skills load", Args: "<name>", Description: "load a skill into conversation context", TakesArgs: true, HasSubs: true},
			{Name: "/skills reload", Description: "reload skills from disk"},
		}
	case "/provider", "/providers", "/p":
		return []SlashCommandItem{
			{Name: "/provider", Description: "open interactive provider manager"},
			{Name: "/provider list", Description: "list configured provider profiles"},
			{Name: "/provider select", Args: "<name>", Description: "switch active provider profile", TakesArgs: true, HasSubs: true},
			{Name: "/provider add", Args: "<name>", Description: "add a new provider profile", TakesArgs: true},
			{Name: "/provider model", Args: "<name>", Description: "set model for active provider", TakesArgs: true},
			{Name: "/provider timeout", Args: "<sec>", Description: "set HTTP timeout for active provider", TakesArgs: true},
			{Name: "/provider remove", Args: "<name>", Description: "remove a saved provider profile", TakesArgs: true, HasSubs: true},
		}
	case "/session", "/sessions":
		return []SlashCommandItem{
			{Name: "/session list", Description: "list saved conversation sessions"},
			{Name: "/session new", Description: "start a new conversation session"},
			{Name: "/session load", Args: "[id]", Description: "pick or load a saved session"},
			{Name: "/session tree", Description: "visualize session branches"},
			{Name: "/session branch", Args: "<id>", Description: "switch to another branch entry", TakesArgs: true},
			{Name: "/session rename", Args: "<name>", Description: "rename current session", TakesArgs: true},
			{Name: "/session clear", Description: "clear current session history"},
		}
	case "/tools", "/tool":
		return []SlashCommandItem{
			{Name: "/tools", Description: "open interactive tools menu"},
			{Name: "/tools list", Description: "list all tools and their enabled status"},
			{Name: "/tools toggle", Args: "<name>", Description: "toggle a tool on or off", TakesArgs: true, HasSubs: true},
			{Name: "/tools enable", Args: "<name|all>", Description: "enable a specific tool or all tools", TakesArgs: true, HasSubs: true},
			{Name: "/tools disable", Args: "<name>", Description: "disable a specific tool", TakesArgs: true, HasSubs: true},
			{Name: "/tools reset", Description: "restore default tool availability"},
		}
	case "/thinking", "/reasoning":
		return []SlashCommandItem{
			{Name: "/thinking off", Description: "disable model reasoning"},
			{Name: "/thinking low", Description: "light reasoning budget"},
			{Name: "/thinking medium", Description: "balanced reasoning budget"},
			{Name: "/thinking high", Description: "deep reasoning budget"},
			{Name: "/thinking max", Description: "maximum reasoning budget"},
		}
	case "/guided", "/guide":
		return []SlashCommandItem{
			{Name: "/guided on", Description: "enable guided mode"},
			{Name: "/guided off", Description: "disable guided mode"},
			{Name: "/guided status", Description: "inspect current guided mode phase"},
			{Name: "/guided plan", Description: "view or draft the execution plan"},
			{Name: "/guided approve", Description: "approve plan and proceed to execution"},
		}
	case "/nostop", "/loop":
		return []SlashCommandItem{
			{Name: "/nostop setup", Description: "configure pacing interactively"},
			{Name: "/nostop eco", Args: "<sec>", Description: "eco pacing with pause interval", TakesArgs: true},
			{Name: "/nostop balanced", Args: "<sec>", Description: "balanced pacing with pause interval", TakesArgs: true},
			{Name: "/nostop continuous", Args: "<sec>", Description: "continuous pacing with pause interval", TakesArgs: true},
			{Name: "/nostop status", Description: "show current nonstop settings"},
			{Name: "/nostop off", Description: "disable nonstop mode"},
		}
	case "/agent", "/agents":
		return []SlashCommandItem{
			{Name: "/agent list", Description: "list active multi-agent swarm threads"},
			{Name: "/agent spawn", Args: "<name>", Description: "spawn a new worker agent", TakesArgs: true},
			{Name: "/agent join", Args: "<name>", Description: "attach to an existing worker agent", TakesArgs: true},
			{Name: "/agent remove", Args: "<name>", Description: "remove a worker agent", TakesArgs: true},
		}
	case "/task", "/tasks":
		return []SlashCommandItem{
			{Name: "/task list", Description: "list running and finished background tasks"},
			{Name: "/task view", Args: "<id>", Description: "inspect output of a background task", TakesArgs: true, HasSubs: true},
			{Name: "/task stream", Args: "<id>", Description: "follow live output of a background task", TakesArgs: true, HasSubs: true},
			{Name: "/task kill", Args: "<id>", Description: "terminate a running background task", TakesArgs: true, HasSubs: true},
			{Name: "/task remove", Args: "<id>", Description: "remove a finished task entry", TakesArgs: true, HasSubs: true},
			{Name: "/task clear", Description: "clear all finished background tasks"},
		}
	case "/context", "/ctx":
		return []SlashCommandItem{
			{Name: "/context", Description: "show current context window settings"},
			{Name: "/context auto", Description: "enable adaptive context window scaling"},
			{Name: "/context off", Description: "disable adaptive context window scaling"},
		}
	case "/find":
		return []SlashCommandItem{
			{Name: "/find next", Description: "jump to next search match"},
			{Name: "/find prev", Description: "jump to previous search match"},
			{Name: "/find clear", Description: "clear active search highlights"},
		}
	case "/changes":
		return []SlashCommandItem{
			{Name: "/changes", Description: "summarize recorded file edits and git status"},
			{Name: "/changes diff", Description: "show unified diff of session changes"},
		}
	case "/doctor":
		return []SlashCommandItem{
			{Name: "/doctor", Description: "run full environment and provider diagnostics"},
			{Name: "/doctor offline", Description: "run local diagnostics without network calls"},
		}
	case "/queue", "/queues":
		return []SlashCommandItem{
			{Name: "/queue list", Description: "view queued prompts"},
			{Name: "/queue undo", Description: "remove the newest queued prompt"},
			{Name: "/queue clear", Description: "clear all queued prompts"},
		}
	default:
		return nil
	}
}

// FilterSlashCommands filters built-in, subcommand, and dynamic slash commands for the current prompt query.
func FilterSlashCommands(
	query string,
	extraTop []SlashCommandItem,
	dynamicSubs func(query string) []SlashCommandItem,
) ([]SlashCommandItem, string) {
	if !strings.HasPrefix(query, "/") || strings.ContainsAny(query, "\r\n") {
		return nil, ""
	}
	lowerQuery := strings.ToLower(query)

	// Subcommand / option mode when a space follows the top-level slash command.
	if spaceIdx := strings.IndexByte(query, ' '); spaceIdx > 0 {
		cmd := strings.ToLower(query[:spaceIdx])
		var candidates []SlashCommandItem
		seen := make(map[string]bool)
		addCandidate := func(it SlashCommandItem) {
			key := strings.ToLower(strings.TrimRight(it.Name, " "))
			if seen[key] {
				return
			}
			seen[key] = true
			candidates = append(candidates, it)
		}
		for _, it := range builtinSubcommands(cmd) {
			addCandidate(it)
		}
		if dynamicSubs != nil {
			for _, it := range dynamicSubs(query) {
				addCandidate(it)
			}
		}
		if len(candidates) == 0 {
			return nil, ""
		}

		var prefixMatches, subMatches []SlashCommandItem
		restRaw := lowerQuery[spaceIdx+1:]
		rest := strings.TrimSpace(restRaw)
		trailingSpace := strings.HasSuffix(lowerQuery, " ")
		for _, it := range candidates {
			nameLower := strings.ToLower(it.Name)
			if rest == "" {
				prefixMatches = append(prefixMatches, it)
				continue
			}
			if strings.HasPrefix(nameLower, lowerQuery) {
				prefixMatches = append(prefixMatches, it)
			} else if !trailingSpace && strings.Contains(nameLower, rest) && !strings.Contains(rest, " ") {
				subMatches = append(subMatches, it)
			}
		}
		matched := append(prefixMatches, subMatches...)
		if len(matched) == 0 {
			return nil, ""
		}
		return matched, cmd
	}

	// Top-level command mode (e.g. "/", "/co", "/sk").
	seen := make(map[string]bool, len(builtinSlashCommands)+len(extraTop))
	all := make([]SlashCommandItem, 0, len(builtinSlashCommands)+len(extraTop))
	for _, it := range builtinSlashCommands {
		key := strings.ToLower(it.Name)
		seen[key] = true
		all = append(all, it)
	}
	for _, it := range extraTop {
		key := strings.ToLower(it.Name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		all = append(all, it)
	}

	if lowerQuery == "/" {
		return all, "/commands"
	}

	needle := lowerQuery[1:]
	var prefixMatches, nameSubMatches, descMatches []SlashCommandItem
	for _, it := range all {
		nameLower := strings.ToLower(it.Name)
		if strings.HasPrefix(nameLower, lowerQuery) {
			prefixMatches = append(prefixMatches, it)
		} else if strings.Contains(nameLower, needle) {
			nameSubMatches = append(nameSubMatches, it)
		} else if len(needle) >= 2 && strings.Contains(strings.ToLower(it.Description), needle) {
			descMatches = append(descMatches, it)
		}
	}
	matched := make([]SlashCommandItem, 0, len(prefixMatches)+len(nameSubMatches)+len(descMatches))
	matched = append(matched, prefixMatches...)
	matched = append(matched, nameSubMatches...)
	matched = append(matched, descMatches...)
	if len(matched) == 0 {
		return nil, ""
	}
	return matched, "/commands"
}

// RenderSlashMenuRows renders the inline slash-command dropdown rows with static
// command and description columns separated by a fixed divider.
func RenderSlashMenuRows(
	items []SlashCommandItem,
	headerTitle string,
	selected, scrollOffset, width, maxVisible int,
	theme style.UITheme,
) (rows []string, visibleStart, visibleCount int) {
	if len(items) == 0 || width <= 4 || maxVisible <= 0 {
		return nil, 0, 0
	}
	available := max(1, width-1)
	visibleCount = min(len(items), maxVisible)
	selected = min(max(0, selected), len(items)-1)

	visibleStart = scrollOffset
	if visibleStart > selected {
		visibleStart = selected
	}
	if selected >= visibleStart+visibleCount {
		visibleStart = selected - visibleCount + 1
	}
	maxStart := max(0, len(items)-visibleCount)
	visibleStart = min(max(0, visibleStart), maxStart)

	borderStyle := style.NewStyle().Foreground(theme.Border)
	headerLabelStyle := style.NewStyle().Foreground(theme.TextMuted).Bold(true)
	counterStyle := style.NewStyle().Foreground(theme.TextMuted)

	if headerTitle == "" {
		headerTitle = "/commands"
	}
	var counterRaw string
	if len(items) > visibleCount {
		counterRaw = fmt.Sprintf(" %d-%d/%d ", visibleStart+1, visibleStart+visibleCount, len(items))
	} else {
		counterRaw = fmt.Sprintf(" %d ", len(items))
	}

	// Fixed column widths based solely on terminal width so scrolling never shifts columns.
	// Layout per item row:
	//   [2 cells: "▹ " / "  "] [cmdColWidth cells] [3 cells: " │ "] [descWidth cells]
	// Total up to divider '│' is (2 + cmdColWidth + 1) = cmdColWidth + 3 cells,
	// placing '│' and header '┬' at exact 1-based column (cmdColWidth + 4).
	showDivider := available >= 36
	var cmdColWidth, descWidth int
	if showDivider {
		cmdColWidth = min(26, max(16, available*30/100))
		if cmdColWidth > available-14 {
			cmdColWidth = max(12, available-14)
		}
		descWidth = max(0, available-cmdColWidth-5)
	} else {
		cmdColWidth = max(1, available-2)
		descWidth = 0
	}

	var headerLine string
	if showDivider {
		leftSpan := cmdColWidth + 3 // cells before '┬'
		leftLabel := style.TruncateCells(headerTitle, max(1, leftSpan-6))
		leftText := "─── " + leftLabel + " "
		leftDash := max(1, leftSpan-style.CellWidth(leftText))
		leftSection := borderStyle.Render("─── ") + headerLabelStyle.Render(leftLabel) + " " +
			borderStyle.Render(strings.Repeat("─", leftDash))

		rightSpan := max(0, available-leftSpan-1) // cells after '┬'
		descLabel := "description"
		descPrefix := "─── " + descLabel + " "
		counterW := style.CellWidth(counterRaw)
		tailDashes := 3
		if rightSpan >= style.CellWidth(descPrefix)+counterW+tailDashes+1 {
			midDash := rightSpan - style.CellWidth(descPrefix) - counterW - tailDashes
			rightSection := borderStyle.Render("─── ") + headerLabelStyle.Render(descLabel) + " " +
				borderStyle.Render(strings.Repeat("─", midDash)) +
				counterStyle.Render(counterRaw) +
				borderStyle.Render(strings.Repeat("─", tailDashes))
			headerLine = leftSection + borderStyle.Render("┬") + rightSection
		} else if rightSpan >= counterW+tailDashes+1 {
			midDash := rightSpan - counterW - tailDashes
			rightSection := borderStyle.Render(strings.Repeat("─", midDash)) +
				counterStyle.Render(counterRaw) +
				borderStyle.Render(strings.Repeat("─", tailDashes))
			headerLine = leftSection + borderStyle.Render("┬") + rightSection
		} else {
			headerLine = leftSection + borderStyle.Render("┬"+strings.Repeat("─", rightSpan))
		}
	} else {
		leftRaw := "─── " + headerTitle + " "
		leftW := style.CellWidth(leftRaw)
		rightW := style.CellWidth(counterRaw) + 3
		if available > leftW+rightW+1 {
			midDashes := strings.Repeat("─", available-leftW-rightW)
			headerLine = borderStyle.Render("─── ") + headerLabelStyle.Render(headerTitle) + " " +
				borderStyle.Render(midDashes) + counterStyle.Render(counterRaw) + borderStyle.Render("───")
		} else {
			headerLine = borderStyle.Render(style.TruncateCells(leftRaw+strings.Repeat("─", available), available))
		}
	}

	window := items[visibleStart : visibleStart+visibleCount]

	selIndicatorStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	selCmdStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	selArgsStyle := style.NewStyle().Foreground(theme.Secondary)
	selDescStyle := style.NewStyle().Foreground(theme.Text)

	cmdStyle := style.NewStyle().Foreground(theme.Primary)
	argsStyle := style.NewStyle().Foreground(theme.TextMuted)
	descStyle := style.NewStyle().Foreground(theme.TextMuted)
	divider := borderStyle.Render(" │ ")

	rows = make([]string, 0, visibleCount+1)
	rows = append(rows, headerLine)

	for i, it := range window {
		isSelected := visibleStart+i == selected
		prefix := "  "
		cStyle, aStyle, dStyle := cmdStyle, argsStyle, descStyle
		if isSelected {
			prefix = selIndicatorStyle.Render("▹ ")
			cStyle, aStyle, dStyle = selCmdStyle, selArgsStyle, selDescStyle
		}

		name := it.Name
		rawCmdW := style.CellWidth(name)
		var cmdPart string
		if rawCmdW > cmdColWidth {
			truncated := style.TruncateCells(name, max(1, cmdColWidth-1)) + "…"
			cmdPart = cStyle.Render(truncated)
			rawCmdW = style.CellWidth(truncated)
		} else {
			cmdPart = cStyle.Render(name)
			if it.Args != "" {
				rem := cmdColWidth - rawCmdW - 1
				if rem >= style.CellWidth(it.Args) {
					cmdPart += " " + aStyle.Render(it.Args)
					rawCmdW += 1 + style.CellWidth(it.Args)
				} else if rem >= 4 {
					truncArgs := style.TruncateCells(it.Args, rem-1) + "…"
					cmdPart += " " + aStyle.Render(truncArgs)
					rawCmdW += 1 + style.CellWidth(truncArgs)
				}
			}
		}

		if !showDivider {
			rows = append(rows, prefix+cmdPart)
			continue
		}

		pad := ""
		if padCells := cmdColWidth - rawCmdW; padCells > 0 {
			pad = strings.Repeat(" ", padCells)
		}

		descPart := ""
		if descWidth > 2 && it.Description != "" {
			desc := SanitizeTerminalText(it.Description)
			if style.CellWidth(desc) > descWidth {
				desc = style.TruncateCells(desc, max(1, descWidth-1)) + "…"
			}
			descPart = dStyle.Render(desc)
		}

		rows = append(rows, prefix+cmdPart+pad+divider+descPart)
	}
	return rows, visibleStart, visibleCount
}
