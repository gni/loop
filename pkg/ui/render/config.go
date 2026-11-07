package render

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RenderConfig(w io.Writer, cfg *config.Config, theme style.UITheme) {
	termW, _ := terminal.GetDimensions()
	if termW <= 0 {
		termW = 80
	}

	borderStyle := style.NewStyle().
		Border(style.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(1, 2).
		Margin(0, 0).
		MaxWidth(termW)

	titleStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true)

	keyStyle := style.NewStyle().
		Foreground(theme.Secondary)

	valStyle := style.NewStyle().
		Foreground(theme.Text)

	type kvPair struct {
		key string
		val string
	}
	pairs := make([]kvPair, 0, len(config.SettingKeys()))
	for _, key := range config.SettingKeys() {
		value, _ := cfg.SettingValue(key)
		if key == "providers" {
			value = fmt.Sprintf("%d configured", len(cfg.Providers))
		}
		if key == "mcp_servers" {
			value = fmt.Sprintf("%d configured", len(cfg.MCPServers))
		}
		if key == "api_key" && value != "" {
			value = "<set>"
		}
		value = strings.Join(strings.Fields(value), " ")
		valueWidth := max(1, termW-38)
		if termW >= 140 {
			valueWidth = max(1, (termW-10)/2-30)
		}
		value = style.TruncateRunes(value, valueWidth)
		pairs = append(pairs, kvPair{key + ":", valStyle.Render(value)})
	}

	formatCell := func(p kvPair) string {
		kPad := 29 - utf8.RuneCountInString(p.key)
		if kPad < 1 {
			kPad = 1
		}
		return fmt.Sprintf("%s%s%s", keyStyle.Render(p.key), strings.Repeat(" ", kPad), p.val)
	}

	var rowsBuilder strings.Builder
	if termW >= 140 {
		rows := (len(pairs) + 1) / 2
		colWidth := (termW - 10) / 2
		if colWidth < 34 {
			colWidth = 34
		}
		sep := style.NewStyle().Foreground(theme.Border).Render(" │ ")

		for r := 0; r < rows; r++ {
			left := formatCell(pairs[r])
			if r+rows < len(pairs) {
				right := formatCell(pairs[r+rows])
				leftVis := utf8.RuneCountInString(style.StripAnsi(left))
				pad := ""
				if leftVis < colWidth {
					pad = strings.Repeat(" ", colWidth-leftVis)
				}
				rowsBuilder.WriteString(fmt.Sprintf("  %s%s%s%s\n", left, pad, sep, right))
			} else {
				rowsBuilder.WriteString(fmt.Sprintf("  %s\n", left))
			}
		}
	} else {
		for _, p := range pairs {
			rowsBuilder.WriteString(fmt.Sprintf("  %s\n", formatCell(p)))
		}
	}

	configStr := fmt.Sprintf(
		"%s\n\n%s\ntip: change any setting via: /config <key> <value> (e.g. /guided on)",
		titleStyle.Render("loop runtime settings"),
		rowsBuilder.String(),
	)

	fmt.Fprintln(w, borderStyle.Render(configStr))
}
