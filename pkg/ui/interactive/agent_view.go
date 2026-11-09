package interactive

import (
	"fmt"
	"strings"

	"loop/pkg/agent/swarm"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func agentPanelRow(text string, width int) string {
	text = style.TruncateCells(text, width)
	return text + strings.Repeat(" ", max(0, width-style.CellWidth(text)))
}

func renderAgentManager(mam *swarm.MultiAgentManager, names []string, active string, selected, width, height int, theme style.UITheme, notice string) string {
	width = max(1, width-2) // Leave the terminal's last column unused.
	height = max(1, height)
	title := style.NewStyle().Foreground(theme.Primary).Bold(true)
	muted := style.NewStyle().Foreground(theme.TextMuted)
	text := style.NewStyle().Foreground(theme.Text)
	border := style.NewStyle().Foreground(theme.Border)
	activeStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
	focused := names[selected]
	isActive := focused == active || (focused == "base" && active == "")
	if active == "" {
		active = "base"
	}

	rows := []string{
		title.Render("Agents") + muted.Render(fmt.Sprintf("  /  %d total", len(names))),
		muted.Render("Chatting with ") + activeStyle.Render(menuDisplayText(active)),
		"",
	}
	bodyHeight := max(1, height-8)
	wide := width >= 86
	listWidth := width
	if wide {
		listWidth = min(34, width/3)
	}
	listHeight := bodyHeight
	if !wide {
		listHeight = max(1, bodyHeight/2)
	}
	// Keep the selected agent visible, regardless of the list size.
	first := max(0, selected-listHeight+1)
	first = min(first, max(0, len(names)-listHeight))
	left := make([]string, 0, listHeight)
	for i := first; i < len(names) && len(left) < listHeight; i++ {
		name := names[i]
		current := name == active
		marker := "  "
		nameStyle := text
		if i == selected {
			marker = "› "
			nameStyle = title
		}
		badge := ""
		if current {
			badge = "  ● active"
		}
		available := max(1, listWidth-style.CellWidth(marker+badge))
		label := style.TruncateCells(menuDisplayText(name), available)
		row := nameStyle.Render(marker + label)
		if current {
			row += activeStyle.Render(badge)
		}
		left = append(left, row)
	}
	detailWidth := width
	if wide {
		detailWidth = width - listWidth - 3
	}
	heading := title.Render(menuDisplayText(focused))
	if isActive {
		heading += activeStyle.Render("  ● active chat")
	}
	parent, skills, instructions := "", "All active skills", ""
	if focused == "base" {
		instructions = "Your main agent coordinates work and keeps the conversation in one place. Create a separate agent when a task needs its own focus."
	} else {
		parent = mam.GetParentName(focused)
		if parent == "" {
			parent = "base"
		}
		assigned, _ := mam.ListAgentSkills(focused)
		if len(assigned) > 0 {
			labels := make([]string, 0, len(assigned))
			for _, skill := range assigned {
				labels = append(labels, menuDisplayText(skill.Name))
			}
			skills = strings.Join(labels, ", ")
		}
		instructions = mam.GetAgentSystemPrompt(focused)
	}
	right := []string{heading}
	if focused == "base" {
		right = append(right, muted.Render("Your main conversation"))
	} else {
		right = append(right, muted.Render("Parent  ")+text.Render(menuDisplayText(parent)))
	}
	right = append(right, muted.Render("Skills  ")+text.Render(skills))
	if len(names) == 1 {
		right = append(right, "", text.Render("A focused agent for each part of your work."), muted.Render("Press C to create your first agent."))
	} else if !isActive {
		right = append(right, "", muted.Render("Enter to continue this conversation"))
	}
	section := "INSTRUCTIONS"
	if focused == "base" {
		section = "ABOUT"
	}
	right = append(right, "", muted.Render(section))
	available := bodyHeight - len(right)
	if !wide {
		available = bodyHeight - len(left) - 1 - len(right)
	}
	wrapped := render.WrapText(menuDisplayText(instructions), max(1, detailWidth))
	for i := 0; i < len(wrapped) && i < available; i++ {
		line := wrapped[i]
		if i == available-1 && i+1 < len(wrapped) {
			line = style.TruncateCells(line, max(1, detailWidth-4)) + " ..."
		}
		right = append(right, text.Render(line))
	}
	if wide {
		for i := 0; i < bodyHeight; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			rows = append(rows, agentPanelRow(l, listWidth)+border.Render(" │ ")+r)
		}
	} else {
		rows = append(rows, left...)
		rows = append(rows, border.Render(strings.Repeat("─", width)))
		rows = append(rows, right...)
		rows = rows[:min(len(rows), 3+bodyHeight)]
	}
	message := ""
	if notice != "" {
		message = style.NewStyle().Foreground(theme.Error).Render(notice)
	}
	rows = append(rows, message, border.Render(strings.Repeat("─", width)))
	position := fmt.Sprintf("%d/%d", selected+1, len(names))
	rows = append(rows, title.Render("↑↓")+muted.Render(" Select   ")+title.Render("Enter")+muted.Render(" Chat   ")+title.Render("C")+muted.Render(" Create"))
	actions := title.Render("Esc") + muted.Render(" Back")
	if focused != "base" {
		actions = title.Render("D") + muted.Render(" Delete   ") + actions
	}
	rows = append(rows, actions+muted.Render("   "+position))
	if len(rows) > height {
		rows = rows[:height]
	}
	var frame strings.Builder
	frame.WriteString("\x1b[H")
	for i, row := range rows {
		if i > 0 {
			frame.WriteString("\r\n")
		}
		frame.WriteString(style.TruncateCells(row, width))
		frame.WriteString("\x1b[K")
	}
	frame.WriteString("\x1b[J")
	return frame.String()
}
