package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/ui/style"
)

func RenderSkills(w io.Writer, skills []tool.Skill, theme style.UITheme) {
	if len(skills) == 0 {
		fmt.Fprintln(w, "No reference skills found.")
		return
	}

	headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	titleStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	descStyle := style.NewStyle().Foreground(theme.Text)
	pathStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)

	width := max(1, renderWidth(w)-6)
	border := headerStyle.Render("│")
	hline := strings.Repeat("─", width+4)
	pad := func(n int) string { return strings.Repeat(" ", max(0, n)) }
	row := func(content string) {
		used := style.CellWidth(content)
		fmt.Fprintf(w, "%s  %s%s  %s\n", border, content, pad(width-used), border)
	}

	wrapCellLines := func(text string, firstLimit, nextLimit int) []string {
		text = strings.Join(strings.Fields(SanitizeTerminalText(text)), " ")
		if text == "" {
			return []string{""}
		}
		firstLimit = max(1, firstLimit)
		nextLimit = max(1, nextLimit)
		if firstLimit == nextLimit {
			return style.WrapAnsiLine(text, nextLimit)
		}
		// Wrap the first line at firstLimit, then wrap the remainder at nextLimit.
		firstWrapped := style.WrapAnsiLine(text, firstLimit)
		if len(firstWrapped) <= 1 {
			return firstWrapped
		}
		rest := strings.TrimSpace(strings.Join(firstWrapped[1:], " "))
		if rest == "" {
			return firstWrapped[:1]
		}
		return append([]string{firstWrapped[0]}, style.WrapAnsiLine(rest, nextLimit)...)
	}

	hdr := style.TruncateCells("AVAILABLE SKILLS (Reference Guides)", width)
	fmt.Fprintln(w, headerStyle.Render("╭"+hline+"╮"))
	fmt.Fprintln(w, headerStyle.Render("│  "+hdr+pad(width-style.CellWidth(hdr))+"  │"))
	fmt.Fprintln(w, headerStyle.Render("├"+hline+"┤"))

	for i, s := range skills {
		name := strings.Join(strings.Fields(SanitizeTerminalText(s.Name)), " ")
		prefixLen := style.CellWidth(name) + 3
		if prefixLen >= width {
			// Long names need their own rows so the first description width
			// stays positive and the wrapping loop always makes progress.
			for _, line := range wrapCellLines(name+" :", width, width) {
				row(titleStyle.Render(line))
			}
			for _, line := range wrapCellLines(s.Description, width, width) {
				row(descStyle.Render(line))
			}
		} else {
			desc := wrapCellLines(s.Description, width-prefixLen, width)
			row(titleStyle.Render(name) + descStyle.Render(" : "+desc[0]))
			for _, line := range desc[1:] {
				row(descStyle.Render(line))
			}
		}
		for _, p := range wrapCellLines(s.Path, width, width) {
			row(pathStyle.Render(p))
		}
		if i < len(skills)-1 {
			row("")
		}
	}

	fmt.Fprintln(w, headerStyle.Render("╰"+hline+"╯"))
}
