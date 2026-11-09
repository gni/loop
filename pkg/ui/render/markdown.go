package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/ui/style"
)

// GetThinkingStyle returns the style used for thought/reasoning text.
func GetThinkingStyle(theme style.UITheme) style.Style {
	border := theme.Border
	if border == nil {
		border = style.Color("#4C566A")
	}
	return style.NewStyle().Foreground(border).Italic(true)
}

// IsNumberedList detects whether a trimmed line is an ordered list item (e.g. "1. Item").
func IsNumberedList(trimmed string) (bool, string, string) {
	if len(trimmed) < 3 {
		return false, "", ""
	}
	spIdx := strings.Index(trimmed, " ")
	if spIdx == -1 {
		return false, "", ""
	}
	prefix := trimmed[:spIdx]
	if len(prefix) < 2 || !strings.HasSuffix(prefix, ".") {
		return false, "", ""
	}
	numPart := prefix[:len(prefix)-1]
	for _, r := range numPart {
		if r < '0' || r > '9' {
			return false, "", ""
		}
	}
	return true, prefix, trimmed[spIdx+1:]
}

func extractBulletPrefix(trimmed string) (string, string, bool) {
	for _, p := range []string{"- ", "* ", "+ ", "• "} {
		if strings.HasPrefix(trimmed, p) {
			return p, trimmed[len(p):], true
		}
	}
	return "", "", false
}

// MarkdownBlock determines the foreground used by both live and saved Markdown.
type MarkdownBlock uint8

const (
	MarkdownBody MarkdownBlock = iota
	MarkdownHeading
	MarkdownQuote
)

// MarkdownStyle is the single theme-dependent style policy for Markdown spans.
func MarkdownStyle(theme style.UITheme, block MarkdownBlock, headingLevel int, bold, italic, strike, code bool) style.Style {
	current := style.NewStyle().Foreground(theme.Text)
	switch block {
	case MarkdownHeading:
		current = current.Foreground(theme.Secondary).Bold(true)
		if headingLevel == 1 {
			current = current.Underline(true)
		}
	case MarkdownQuote:
		current = current.Foreground(theme.Border).Italic(true)
	}
	if bold {
		if block == MarkdownBody {
			current = current.Foreground(theme.Primary)
		}
		current = current.Bold(true)
	}
	if italic {
		current = current.Italic(true)
	}
	if strike {
		current = current.Strikethrough(true)
	}
	if code {
		current = current.Foreground(theme.Highlight)
	}
	return current
}

// RenderInlineMarkdown parses and renders inline code, bold, and italic markers.
func RenderInlineMarkdown(text string, inThinking bool, theme style.UITheme) string {
	return renderInlineMarkdownBlock(text, inThinking, theme, MarkdownBody, 0)
}

func renderInlineMarkdownBlock(text string, inThinking bool, theme style.UITheme, block MarkdownBlock, level int) string {
	var output strings.Builder
	inline := NewInlineMarkdownWriter(&output, theme, block, level, inThinking)
	inline.Write(text)
	inline.EndLine()
	return output.String()
}

// PrintNormalMarkdownLine renders a single line with markdown blocks (headers, quotes, bullets, lists).
func PrintNormalMarkdownLine(w io.Writer, line string, theme style.UITheme) {
	trimmed := strings.TrimSpace(line)

	if trimmed == "----" {
		fmt.Fprint(w, " ")
		return
	}

	if strings.HasPrefix(trimmed, "#") {
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		if hashes <= 6 && hashes < len(trimmed) && trimmed[hashes] == ' ' {
			headerText := strings.TrimSpace(trimmed[hashes:])
			fmt.Fprint(w, renderInlineMarkdownBlock(headerText, false, theme, MarkdownHeading, hashes))
			return
		}
	}

	if strings.HasPrefix(trimmed, ">") {
		quoteText := strings.TrimSpace(trimmed[1:])
		marker := style.NewStyle().Foreground(theme.Border).Render("┃ ")
		fmt.Fprint(w, marker, renderInlineMarkdownBlock(quoteText, false, theme, MarkdownQuote, 0))
		return
	}

	if _, bulletText, ok := extractBulletPrefix(trimmed); ok {
		bulletSymbol := style.NewStyle().Foreground(theme.Primary).Render("•")
		fmt.Fprintf(w, "  %s %s", bulletSymbol, RenderInlineMarkdown(bulletText, false, theme))
		return
	}

	if ok, numPrefix, listText := IsNumberedList(trimmed); ok {
		numSymbol := style.NewStyle().Foreground(theme.Primary).Render(numPrefix)
		fmt.Fprintf(w, "  %s %s", numSymbol, RenderInlineMarkdown(listText, false, theme))
		return
	}

	fmt.Fprint(w, RenderInlineMarkdown(line, false, theme))
}

func renderMarkdownBlocks(w io.Writer, content string, onCodeLine func(string, string), onTextLine func(string), onTable func([]string, []tableAlignment, []string)) {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	language := "plaintext"
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				language = "plaintext"
				if fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))); len(fields) > 0 {
					language = fields[0]
				}
			}
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			onCodeLine(line, language)
		} else {
			if onTable != nil && i+1 < len(lines) {
				if header, align, ok := markdownTableHeader(line, lines[i+1]); ok {
					end := i + 2
					for end < len(lines) {
						if _, ok := markdownTableRow(lines[end]); !ok {
							break
						}
						end++
					}
					onTable(header, align, lines[i+2:end])
					i = end - 1
					if i < len(lines)-1 {
						fmt.Fprintln(w)
					}
					continue
				}
			}
			// Let the terminal wrap completed logical lines. Splitting source
			// before parsing can break emphasis spans across wrapped rows.
			onTextLine(line)
		}
		if i < len(lines)-1 {
			fmt.Fprintln(w)
		}
	}
}

// RenderRawMarkdownContent prints the original Markdown without interpreting it.
func RenderRawMarkdownContent(w io.Writer, content string, _ style.UITheme) {
	fmt.Fprint(w, content)
}

// RenderMarkdownContent renders multiline markdown including code blocks and wrapped lines.
func RenderMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	renderMarkdownBlocks(
		w,
		content,
		func(line, language string) {
			// Live fenced-code output is highlighted line by line. Use the
			// same Chroma formatter and language on history redraw.
			_ = HighlightWithoutTrailingNewline(w, line, language, theme.ChromaStyle)
		},
		func(line string) { PrintNormalMarkdownLine(w, line, theme) },
		func(header []string, align []tableAlignment, rows []string) {
			table := newMarkdownTable(w, header, align, theme)
			table.start(w)
			for _, row := range rows {
				cells, _ := markdownTableRow(row)
				table.writeRow(w, cells)
			}
			table.finish(w)
		},
	)
}

// RenderReasoningMarkdownContent renders reasoning/thinking markdown content.
func RenderReasoningMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	dimStyle := GetThinkingStyle(theme)
	renderMarkdownBlocks(
		w,
		content,
		func(line, _ string) { fmt.Fprint(w, dimStyle.Render("  "+line)) },
		func(line string) { fmt.Fprint(w, dimStyle.Render(line)) },
		nil,
	)
}
