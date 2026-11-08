package render

import (
	"fmt"
	"io"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
	"sort"
	"strings"
)

func renderStructuredSummary(w io.Writer, value interface{}, theme style.UITheme) {
	width, _ := terminal.GetDimensions()
	describe := func(value interface{}) string {
		switch v := value.(type) {
		case string:
			return strings.Join(strings.Fields(SanitizeTerminalText(v)), " ")
		case []interface{}:
			return fmt.Sprintf("%d items", len(v))
		case map[string]interface{}:
			for _, key := range []string{"name", "title", "status", "message", "path"} {
				if text, ok := v[key].(string); ok {
					return SanitizeTerminalText(text)
				}
			}
			return fmt.Sprintf("%d fields", len(v))
		case nil:
			return "none"
		default:
			return fmt.Sprint(v)
		}
	}
	line := func(label string, value interface{}) {
		text := "⎿  " + label + describe(value)
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Text).Render(style.TruncateCells(text, max(1, width-1))))
	}
	switch v := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			line("", "Done")
			return
		}
		for _, key := range keys[:min(6, len(keys))] {
			label := strings.ReplaceAll(SanitizeTerminalText(key), "_", " ")
			line(label+": ", v[key])
		}
	case []interface{}:
		if len(v) == 0 {
			line("", "No results")
			return
		}
		line("", v)
		for _, item := range v[:min(3, len(v))] {
			line("  ", item)
		}
	default:
		line("", value)
	}
	fmt.Fprintln(w, style.NewStyle().Foreground(theme.TextMuted).Render("   Ctrl+O to inspect full result"))
}
