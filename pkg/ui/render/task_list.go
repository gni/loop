package render

import (
	"fmt"
	"io"
	"loop/pkg/agent"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
	"strings"
	"time"
)

func RenderBackgroundTasks(w io.Writer, tasks []agent.TaskInfo, theme style.UITheme) {
	if len(tasks) == 0 {
		fmt.Fprintln(w, "No background tasks. Start a command in the background to track it here.")
		return
	}
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.Primary).Bold(true).Render("Background tasks"))
	width, _ := terminal.GetDimensions()
	for _, item := range tasks {
		label, mark, tint := taskStateAppearance(item.Status, theme)
		if item.Status == "killed" {
			label, mark, tint = "Stopped", "-", theme.TextMuted
		}
		line := fmt.Sprintf("%s %s · %s · %s · %s", mark, label, item.ID, item.Duration.Round(time.Second), humanBytes(item.BytesOut))
		fmt.Fprintln(w, style.NewStyle().Foreground(tint).Render(style.TruncateCells(line, max(1, width-2))))
		command := strings.Join(strings.Fields(SanitizeTerminalText(item.Command)), " ")
		fmt.Fprintln(w, "  "+style.NewStyle().Foreground(theme.TextMuted).Render(style.TruncateCells(command, max(1, width-3))))
	}
	fmt.Fprintln(w, "/task view <id> · /task stream <id> · /stop <id>")
}

func RenderBackgroundTask(w io.Writer, id, command, status, output string, collapse bool, theme style.UITheme) {
	label, mark, tint := taskStateAppearance(status, theme)
	if status == "killed" {
		label, mark, tint = "Stopped", "-", theme.TextMuted
	}
	fmt.Fprintln(w, style.NewStyle().Foreground(tint).Bold(true).Render("⎿  "+mark+" "+label)+" · "+SanitizeTerminalText(id))
	width, _ := terminal.GetDimensions()
	if command != "" {
		fmt.Fprintln(w, "   "+style.NewStyle().Foreground(theme.TextMuted).Render(style.TruncateCells(strings.Join(strings.Fields(SanitizeTerminalText(command)), " "), max(1, width-4))))
	}
	output = strings.TrimSpace(SanitizeTerminalText(output))
	if output == "" || output == "(no output)" {
		message := "No output captured."
		if status == "running" {
			message = "No output yet."
		}
		fmt.Fprintln(w, "   "+message)
		return
	}
	lines := ChatHistoryRows(output, max(1, width-4))
	limit := len(lines)
	if collapse {
		limit = min(5, limit)
	}
	for _, line := range lines[:limit] {
		fmt.Fprintln(w, "   "+line)
	}
	if limit < len(lines) {
		renderCollapsedNotice(w, len(lines)-limit, theme)
	}
}

func humanBytes(size int) string {
	if size >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	}
	if size >= 1<<10 {
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}
