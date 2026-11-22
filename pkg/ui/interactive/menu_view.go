package interactive

import (
	"fmt"
	"strings"

	"loop/pkg/ui/style"
)

type settingsLayout struct {
	twoColumns bool
	rows       int
	pageSize   int
}

func compactSettingCell(item *SettingItem, selected bool, width int, theme style.UITheme) string {
	marker := "  "
	tint := theme.Text
	if selected {
		marker = "› "
		tint = theme.Primary
	}
	name := MenuDisplayText(item.Name)
	value := MenuDisplayText(item.Value())
	available := max(1, width-2)
	if value == "" {
		return style.NewStyle().Foreground(tint).Bold(selected).Render(marker + style.TruncateCells(name, available))
	}
	// Keep room for both label and value, even on phone-sized terminals.
	valueWidth := min(style.CellWidth(value), max(4, available/3))
	nameWidth := max(1, available-valueWidth-2)
	label := style.TruncateCells(name, nameWidth)
	val := style.TruncateCells(value, valueWidth)
	return style.NewStyle().Foreground(tint).Bold(selected).Render(marker+label) + ": " + style.NewStyle().Foreground(theme.Secondary).Render(val)
}

// Reserve header, details and controls before assigning rows to settings.
// Compositor clipping prevents auto-wrap and scroll artifacts on redraw.
func renderSettingsMenuRows(title, query string, items []*SettingItem, selected, width, height int, theme style.UITheme, extraRender func(*strings.Builder)) ([]string, settingsLayout) {
	width = max(1, width-1)
	capacity := max(1, height)
	layout := settingsLayout{rows: len(items), pageSize: 1}
	titleStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	muted := style.NewStyle().Foreground(theme.TextMuted)
	if width < 17 || capacity < 5 {
		return []string{style.TruncateCells("Resize; Esc saves", width)}, layout
	}
	position := "0/0"
	if len(items) > 0 {
		position = fmt.Sprintf("%d/%d", selected+1, len(items))
	}
	titleWidth := max(1, width-style.CellWidth(position)-2)
	heading := style.TruncateCells(title, titleWidth)
	header := titleStyle.Render(heading) + strings.Repeat(" ", max(1, width-style.CellWidth(heading)-style.CellWidth(position))) + muted.Render(position)
	search := "type to search"
	if query != "" {
		search = query
	}
	rows := []string{header, muted.Render("/ " + search)}
	detailRows := 0
	if capacity >= 10 {
		detailRows = 2
	}
	var extras []string
	if extraRender != nil && capacity >= 19 && title != "endpoint providers config" {
		var extra strings.Builder
		extraRender(&extra)
		rawExtras := strings.Split(strings.TrimRight(extra.String(), "\r\n"), "\n")
		for i := 0; i < len(rawExtras) && i < 3; i++ {
			extras = append(extras, strings.TrimRight(rawExtras[i], "\r"))
		}
	}
	visible := max(1, capacity-len(rows)-2-detailRows-len(extras))
	layout.twoColumns = title != "endpoint providers config" && width >= 100 && capacity >= 13 && len(items) > 1
	if layout.twoColumns {
		layout.rows = (len(items) + 1) / 2
	}
	layout.pageSize = visible
	if len(items) == 0 {
		rows = append(rows, muted.Render("No matching settings"))
	} else if layout.twoColumns {
		colWidth := (width - 3) / 2
		selectedRow := selected % layout.rows
		start := min(max(0, selectedRow-visible+1), max(0, layout.rows-visible))
		for i := start; i < layout.rows && i < start+visible; i++ {
			left := FormatMenuCell(items[i], i == selected, colWidth, theme)
			right := ""
			if i+layout.rows < len(items) {
				right = FormatMenuCell(items[i+layout.rows], i+layout.rows == selected, colWidth, theme)
			}
			rows = append(rows, PadAnsiCell(style.TruncateCells(left, colWidth), colWidth)+muted.Render(" │ ")+right)
		}
	} else {
		start := min(max(0, selected-visible+1), max(0, len(items)-visible))
		for i := start; i < len(items) && i < start+visible; i++ {
			rows = append(rows, compactSettingCell(items[i], i == selected, width, theme))
		}
	}
	// Keep the footer in a stable position while filtering or changing settings.
	for len(rows) < capacity-2-detailRows-len(extras) {
		rows = append(rows, "")
	}
	if detailRows > 0 {
		description, value := "", ""
		if len(items) > 0 {
			description = MenuDisplayText(items[selected].Description)
			value = MenuDisplayText(items[selected].Value())
		}
		rows = append(rows, muted.Render(description), style.NewStyle().Foreground(theme.Highlight).Render("Value: "+value))
	}
	rows = append(rows, extras...)
	controls := "↑↓ move · Enter edit · Space toggle"
	footer := "Type to search · PgUp/PgDn page · Home/End · Esc save"
	if width < 55 {
		controls = "↑↓ move · Enter edit"
		footer = "Type search · Esc save"
	}
	if width < 25 {
		controls = "↑↓ Enter · Space"
		footer = "Type search · Esc"
	}
	rows = append(rows, muted.Render(controls), muted.Render(footer))
	return rows[:min(len(rows), capacity)], layout
}
