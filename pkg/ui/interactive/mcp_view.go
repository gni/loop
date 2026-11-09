package interactive

import (
	"fmt"
	"sort"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

const mcpMinWidth, mcpMinHeight = 28, 9

func mcpLocal(server config.MCPServerConfig) bool {
	return strings.EqualFold(server.Type, "stdio") || (server.URL == "" && server.Command != "")
}

func mcpAddress(server config.MCPServerConfig) string {
	if mcpLocal(server) {
		return menuDisplayText(server.Command)
	}
	return menuDisplayText(transporthttp.RedactURL(server.URL))
}

func (m *mcpManager) names() []string {
	var names []string
	query := strings.ToLower(m.query)
	for name, server := range m.draft.MCPServers {
		if query == "" || strings.Contains(strings.ToLower(name+" "+mcpAddress(server)), query) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (m *mcpManager) status(name string, live map[string]agent.MCPServerStatus) agent.MCPServerStatus {
	server := m.draft.MCPServers[name]
	if !server.Enabled {
		return agent.MCPServerStatus{State: "disabled"}
	}
	if !m.draft.MCP {
		return agent.MCPServerStatus{State: "paused"}
	}
	saved, exists := m.saved.MCPServers[name]
	if !m.saved.MCP || !exists || !config.MCPServersEqual(map[string]config.MCPServerConfig{name: server}, map[string]config.MCPServerConfig{name: saved}) {
		return agent.MCPServerStatus{State: "pending"}
	}
	if status, exists := live[name]; exists {
		return status
	}
	if !server.AlwaysLoad {
		return agent.MCPServerStatus{State: "deferred"}
	}
	return agent.MCPServerStatus{State: "disconnected"}
}

func mcpStatusText(status agent.MCPServerStatus, theme style.UITheme) string {
	label, tint := "Disconnected", theme.TextMuted
	switch status.State {
	case "connected":
		label, tint = "Connected", theme.Success
	case "connecting":
		label, tint = "Connecting", theme.Warning
	case "failed":
		label, tint = "Failed", theme.Error
	case "disabled":
		label = "Disabled"
	case "paused":
		label = "Paused"
	case "pending":
		label, tint = "Pending", theme.Warning
	case "deferred":
		label, tint = "On demand", theme.Secondary
	}
	return style.NewStyle().Foreground(tint).Render(label)
}

func mcpControlRows(width int) []string {
	if width >= 78 {
		return []string{"↑↓ Select   Enter Configure   Space Toggle   A Add   D Remove", "M MCP on/off   R Reconnect all   / Search   Esc Save   ^C Cancel"}
	}
	if width >= 48 {
		return []string{"↑↓ Select   Enter Configure   Space Toggle", "A Add   D Remove   / Search   ^C Cancel", "M MCP on/off   R Reconnect all   Esc Save"}
	}
	if width >= 30 {
		return []string{"↑↓ Move   Enter Edit   / Find", "Space Toggle   A Add   D Del", "M On/Off   R Reconnect all", "Esc Save   Ctrl+C Cancel"}
	}
	return []string{"↑↓ Move Enter Edit / Find", "Space Toggle A Add D Del", "M MCP R Retry all", "Esc Save Ctrl+C Cancel"}
}

func renderMCPManager(m *mcpManager, names []string, statuses map[string]agent.MCPServerStatus, width, height int, theme style.UITheme) (string, int) {
	width, capacity := max(1, width-2), max(1, height-1)
	title := style.NewStyle().Foreground(theme.Primary).Bold(true)
	muted := style.NewStyle().Foreground(theme.TextMuted)
	border := style.NewStyle().Foreground(theme.Border)
	if width < mcpMinWidth-2 || capacity < mcpMinHeight-1 {
		return mcpFrame([]string{title.Render("MCP"), muted.Render("Resize · Esc saves")}, width, capacity), 1
	}
	master, tint := " OFF ", theme.TextMuted
	if m.draft.MCP {
		master, tint = " ON ", theme.Success
	}
	badge := style.NewStyle().Foreground(tint).Bold(true).Render("[" + strings.TrimSpace(master) + "]")
	heading := title.Render("MCP") + muted.Render(" / Connections")
	rows := []string{heading + strings.Repeat(" ", max(1, width-style.CellWidth(heading)-style.CellWidth(badge))) + badge}
	enabled, connected := 0, 0
	for name, server := range m.draft.MCPServers {
		if server.Enabled {
			enabled++
		}
		if m.status(name, statuses).State == "connected" {
			connected++
		}
	}
	if capacity >= 12 {
		rows = append(rows, muted.Render(fmt.Sprintf("%d servers  ·  %d enabled  ·  %d connected", len(m.draft.MCPServers), enabled, connected)), border.Render(strings.Repeat("─", width)))
	}
	filter := "SERVERS"
	if m.searching || m.query != "" {
		filter = "/ " + menuDisplayText(m.query)
		if m.searching {
			filter += "_"
		}
	}
	rows = append(rows, muted.Render(filter))
	controls := mcpControlRows(width)
	showNotice := capacity >= len(rows)+len(controls)+2
	noticeRows := 0
	if showNotice {
		noticeRows = 1
	}
	bodyHeight := max(1, capacity-len(rows)-len(controls)-noticeRows)
	wide := width >= 86 && bodyHeight >= 5
	listWidth, listHeight := width, bodyHeight
	if wide {
		listWidth = min(36, width/3)
		rows[len(rows)-1] = padAnsiCell(muted.Render(filter), listWidth) + border.Render(" │ ") + muted.Render("SERVER DETAILS")
	} else if bodyHeight >= 6 {
		listHeight = max(1, min(len(names), bodyHeight/2-1))
	}
	selected := max(0, min(m.selected, len(names)-1))
	start := min(max(0, selected-listHeight+1), max(0, len(names)-listHeight))
	left := make([]string, 0, listHeight)
	for index := start; index < len(names) && index < start+listHeight; index++ {
		name := names[index]
		state := mcpStatusText(m.status(name, statuses), theme)
		marker, label := "  ", style.NewStyle().Foreground(theme.Text)
		if index == selected {
			marker, label = "› ", title
		}
		nameWidth := max(1, listWidth-style.CellWidth(state)-3)
		text := label.Render(marker + style.TruncateCells(menuDisplayText(name), nameWidth))
		left = append(left, text+strings.Repeat(" ", max(1, listWidth-style.CellWidth(text)-style.CellWidth(state)))+state)
	}
	if len(names) == 0 {
		message := "No servers yet."
		if m.query != "" {
			message = "No matching servers."
		}
		left = append(left, muted.Render(message))
	}
	right := []string{title.Render("Connect your tools"), "", muted.Render("Add an MCP server to make its tools available to Loop."), "", title.Render("A") + muted.Render(" Add your first server")}
	if len(m.draft.MCPServers) > 0 && len(names) == 0 {
		right = []string{title.Render("No matching servers"), "", muted.Render("Change your search, or press Esc to show all servers.")}
	}
	if len(names) > 0 {
		detailWidth := width
		if wide {
			detailWidth -= listWidth + 3
		}
		right = mcpServerDetails(m, names[selected], statuses, detailWidth, theme)
		if !wide {
			status := m.status(names[selected], statuses)
			server := m.draft.MCPServers[names[selected]]
			right = []string{title.Render(menuDisplayText(names[selected])) + " · " + mcpStatusText(status, theme), style.NewStyle().Foreground(theme.Text).Render(mcpAddress(server))}
			if mcpLocal(server) {
				right = append(right, muted.Render(fmt.Sprintf("%d arguments · %d environment values", len(server.Args), len(server.Env))))
			} else {
				right = append(right, muted.Render(fmt.Sprintf("%d tools · %d headers", status.Tools, len(server.Headers))))
			}
			if status.Error != "" {
				right = append(right, style.NewStyle().Foreground(theme.Error).Render(menuDisplayText(status.Error)))
			}
		}
	}
	if wide {
		for index := 0; index < bodyHeight; index++ {
			l, r := "", ""
			if index < len(left) {
				l = left[index]
			}
			if index < len(right) {
				r = right[index]
			}
			rows = append(rows, padAnsiCell(l, listWidth)+border.Render(" │ ")+r)
		}
	} else {
		body := append([]string(nil), left...)
		if bodyHeight >= 6 {
			body = append(body, border.Render(strings.Repeat("─", width)))
			body = append(body, right...)
		}
		for len(body) < bodyHeight {
			body = append(body, "")
		}
		rows = append(rows, body[:min(len(body), bodyHeight)]...)
	}
	notice := m.notice
	noticeStyle := muted
	if notice == "" {
		if m.draft.MCP != m.saved.MCP || !config.MCPServersEqual(m.draft.MCPServers, m.saved.MCPServers) {
			notice = "Changes pending · Esc saves · Ctrl+C cancels"
			noticeStyle = style.NewStyle().Foreground(theme.Warning)
		} else {
			notice = "Select a server to view its connection."
		}
	} else if m.failed {
		noticeStyle = style.NewStyle().Foreground(theme.Error)
	} else if m.busy {
		noticeStyle = title
	} else {
		noticeStyle = style.NewStyle().Foreground(theme.Success)
	}
	if showNotice {
		rows = append(rows, noticeStyle.Render(menuDisplayText(notice)))
	}
	for _, line := range controls {
		var parts []string
		for _, part := range strings.Split(line, "   ") {
			key, label, _ := strings.Cut(part, " ")
			parts = append(parts, title.Render(key)+muted.Render(" "+label))
		}
		rows = append(rows, strings.Join(parts, "   "))
	}
	return mcpFrame(rows, width, capacity), listHeight
}

func mcpServerDetails(m *mcpManager, name string, statuses map[string]agent.MCPServerStatus, width int, theme style.UITheme) []string {
	server, status := m.draft.MCPServers[name], m.status(name, statuses)
	title := style.NewStyle().Foreground(theme.Primary).Bold(true)
	muted := style.NewStyle().Foreground(theme.TextMuted)
	text := style.NewStyle().Foreground(theme.Text)
	rows := []string{title.Render(menuDisplayText(name)), mcpStatusText(status, theme)}
	if status.State == "failed" {
		for index, line := range render.WrapText(menuDisplayText(status.Error), max(1, width)) {
			if index == 3 {
				break
			}
			rows = append(rows, style.NewStyle().Foreground(theme.Error).Render(line))
		}
	}
	transport := "HTTP"
	if strings.EqualFold(server.Type, "sse") {
		transport = "SSE"
	}
	if mcpLocal(server) {
		transport = "Local command"
	}
	rows = append(rows, "", muted.Render(transport))
	for _, line := range render.WrapText(mcpAddress(server), max(1, width)) {
		rows = append(rows, text.Render(line))
		if len(rows) >= 6 {
			break
		}
	}
	when := "Connect on demand"
	if server.AlwaysLoad {
		when = "Connect at startup"
	}
	rows = append(rows, "", muted.Render("Connection  ")+text.Render(when))
	if status.State == "connected" {
		rows = append(rows, muted.Render("Tools       ")+text.Render(fmt.Sprintf("%d available", status.Tools)))
	}
	if mcpLocal(server) {
		rows = append(rows, muted.Render(fmt.Sprintf("Settings    %d arguments · %d environment values", len(server.Args), len(server.Env))))
	} else {
		rows = append(rows, muted.Render(fmt.Sprintf("Headers     %d configured · values hidden", len(server.Headers))))
	}
	if server.OAuth != nil {
		rows = append(rows, muted.Render("Sign-in     OAuth configured"))
	}
	message := "Enter to configure this server."
	switch status.State {
	case "deferred":
		message = "Connects when needed. R connects enabled servers now."
	case "paused":
		message = "MCP is off. M enables connections; server settings are kept."
	case "disabled":
		message = "This server is off. Space enables it."
	case "pending":
		message = "Esc applies these changes. R saves and reconnects now."
	case "failed":
		message = "R retries enabled servers. Enter opens this connection's settings."
	case "disconnected":
		message = "Connection needs a refresh. R reconnects enabled servers."
	}
	rows = append(rows, "")
	for index, line := range render.WrapText(message, max(1, width)) {
		if index == 3 {
			break
		}
		lineStyle := muted
		if status.State == "failed" {
			lineStyle = style.NewStyle().Foreground(theme.Error)
		}
		rows = append(rows, lineStyle.Render(line))
	}
	return rows
}

func mcpFrame(rows []string, width, height int) string {
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for index, row := range rows[:min(len(rows), height)] {
		if index > 0 {
			frame.WriteString("\r\n")
		}
		frame.WriteString(style.TruncateCells(row, width) + "\x1b[K")
	}
	frame.WriteString("\x1b[J")
	return frame.String()
}
