package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

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

	width := 78
	fd := os.Stdout.Fd()
	if f, ok := w.(*os.File); ok {
		fd = f.Fd()
	}
	if cols, _, err := term.GetSize(int(fd)); err == nil && cols > 42 {
		width = cols - 6
	}

	border := headerStyle.Render("│")
	hline := strings.Repeat("─", width+4)
	pad := func(n int) string { return strings.Repeat(" ", max(0, n)) }
	row := func(content string, used int) {
		fmt.Fprintf(w, "%s  %s%s  %s\n", border, content, pad(width-used), border)
	}

	wrap := func(text string, limit, next int) []string {
		var lines []string
		var cur string
		for _, word := range strings.Fields(text) {
			if (cur == "" && limit < next && len([]rune(word)) > limit) ||
				(cur != "" && len([]rune(cur))+1+len([]rune(word)) > limit) {
				lines, cur, limit = append(lines, cur), "", next
			}
			if cur != "" {
				cur += " "
			}
			cur += word
			for len([]rune(cur)) > limit {
				r := []rune(cur)
				lines, cur, limit = append(lines, string(r[:limit])), string(r[limit:]), next
			}
		}
		return append(lines, cur)
	}

	hdr := "AVAILABLE SKILLS (Reference Guides)"
	fmt.Fprintln(w, headerStyle.Render("╭"+hline+"╮"))
	fmt.Fprintln(w, headerStyle.Render("│  "+hdr+pad(width-len(hdr))+"  │"))
	fmt.Fprintln(w, headerStyle.Render("├"+hline+"┤"))

	for i, s := range skills {
		prefixLen := len([]rune(s.Name)) + 3
		if prefixLen >= width {
			// Long names need their own rows so the first description width
			// stays positive and the wrapping loop always makes progress.
			for _, line := range wrap(s.Name+" :", width, width) {
				row(titleStyle.Render(line), len([]rune(line)))
			}
			for _, line := range wrap(s.Description, width, width) {
				row(descStyle.Render(line), len([]rune(line)))
			}
		} else {
			desc := wrap(s.Description, width-prefixLen, width)
			row(titleStyle.Render(s.Name)+descStyle.Render(" : "+desc[0]), prefixLen+len([]rune(desc[0])))
			for _, line := range desc[1:] {
				row(descStyle.Render(line), len([]rune(line)))
			}
		}
		for _, p := range wrap(s.Path, width, width) {
			row(pathStyle.Render(p), len([]rune(p)))
		}
		if i < len(skills)-1 {
			row("", 0)
		}
	}

	fmt.Fprintln(w, headerStyle.Render("╰"+hline+"╯"))
}
