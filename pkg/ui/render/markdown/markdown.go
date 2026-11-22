package markdown

import (
	"fmt"
	"image/color"
	"io"
	"strings"

	"loop/pkg/ui/render/syntax"
	"loop/pkg/ui/style"
)

func thinkingBaseColor(theme style.UITheme) color.Color {
	if theme.Border != nil {
		return theme.Border
	}
	return style.Color("#4C566A")
}

func thinkingAccentColor(theme style.UITheme) color.Color {
	if theme.TextMuted != nil {
		return theme.TextMuted
	}
	return thinkingBaseColor(theme)
}

// GetThinkingStyle returns the style used for thought/reasoning text.
func GetThinkingStyle(theme style.UITheme) style.Style {
	return style.NewStyle().Foreground(thinkingBaseColor(theme)).Italic(true)
}

// parseOpeningCodeFence checks whether line opens a backtick code fence of length >= 3.
// Per CommonMark, up to 3 leading spaces are allowed and the info string cannot contain backticks.
func parseOpeningCodeFence(line string) (ticks int, language string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return 0, "", false
	}
	for ticks < len(trimmed) && trimmed[ticks] == '`' {
		ticks++
	}
	if ticks < 3 {
		return 0, "", false
	}
	after := trimmed[ticks:]
	if strings.ContainsRune(after, '`') {
		return 0, "", false
	}
	language = "plaintext"
	if fields := strings.Fields(strings.TrimSpace(after)); len(fields) > 0 {
		language = fields[0]
	}
	return ticks, language, true
}

// isClosingCodeFence checks whether line closes a backtick fence opened with openTicks backticks.
func isClosingCodeFence(line string, openTicks int) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	ticks := 0
	for ticks < len(trimmed) && trimmed[ticks] == '`' {
		ticks++
	}
	return ticks >= max(3, openTicks) && strings.TrimSpace(trimmed[ticks:]) == ""
}

// IsNumberedList detects whether a trimmed line is an ordered list item (e.g. "1. Item").
func IsNumberedList(trimmed string) (bool, string, string) {
	if len(trimmed) < 3 {
		return false, "", ""
	}
	before, after, ok := strings.Cut(trimmed, " ")
	if !ok {
		return false, "", ""
	}
	prefix := before
	if len(prefix) < 2 || !strings.HasSuffix(prefix, ".") {
		return false, "", ""
	}
	numPart := prefix[:len(prefix)-1]
	for _, r := range numPart {
		if r < '0' || r > '9' {
			return false, "", ""
		}
	}
	return true, prefix, after
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

// ThinkingMarkdownStyle is the subdued theme-dependent style policy for
// Markdown spans inside agent thinking/reasoning blocks.
func ThinkingMarkdownStyle(theme style.UITheme, block MarkdownBlock, headingLevel int, bold, italic, strike, code bool) style.Style {
	base := thinkingBaseColor(theme)
	accent := thinkingAccentColor(theme)
	current := style.NewStyle().Foreground(base).Italic(true)
	switch block {
	case MarkdownHeading:
		current = style.NewStyle().Foreground(accent).Bold(true).Italic(true)
		if headingLevel == 1 {
			current = current.Underline(true)
		}
	case MarkdownQuote:
		current = style.NewStyle().Foreground(base).Italic(true)
	}
	if bold {
		current = current.Foreground(accent).Bold(true)
	}
	if strike {
		current = current.Strikethrough(true)
	}
	if code {
		current = current.Foreground(accent).Italic(false)
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
	printMarkdownLine(w, line, theme, false)
}

func printMarkdownLine(w io.Writer, line string, theme style.UITheme, thinking bool) {
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
			fmt.Fprint(w, renderInlineMarkdownBlock(headerText, thinking, theme, MarkdownHeading, hashes))
			return
		}
	}

	if strings.HasPrefix(trimmed, ">") {
		quoteText := strings.TrimSpace(trimmed[1:])
		markerColor := theme.Border
		if thinking {
			markerColor = thinkingBaseColor(theme)
		}
		marker := style.NewStyle().Foreground(markerColor).Render("┃ ")
		fmt.Fprint(w, marker, renderInlineMarkdownBlock(quoteText, thinking, theme, MarkdownQuote, 0))
		return
	}

	listMarkerColor := theme.Primary
	if thinking {
		listMarkerColor = thinkingAccentColor(theme)
	}

	if _, bulletText, ok := extractBulletPrefix(trimmed); ok {
		bulletSymbol := style.NewStyle().Foreground(listMarkerColor).Render("•")
		fmt.Fprintf(w, "  %s %s", bulletSymbol, RenderInlineMarkdown(bulletText, thinking, theme))
		return
	}

	if ok, numPrefix, listText := IsNumberedList(trimmed); ok {
		numSymbol := style.NewStyle().Foreground(listMarkerColor).Render(numPrefix)
		fmt.Fprintf(w, "  %s %s", numSymbol, RenderInlineMarkdown(listText, thinking, theme))
		return
	}

	fmt.Fprint(w, RenderInlineMarkdown(line, thinking, theme))
}

func renderMarkdownBlocks(w io.Writer, content string, onCodeLine func(string, string), onTextLine func(string), onTable func([]string, []tableAlignment, []string)) {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	fenceLen := 0
	language := "plaintext"
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !inCodeBlock {
			if ticks, lang, ok := parseOpeningCodeFence(line); ok {
				inCodeBlock = true
				fenceLen = ticks
				language = lang
				continue
			}
		} else if isClosingCodeFence(line, fenceLen) {
			inCodeBlock = false
			fenceLen = 0
			language = "plaintext"
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
			_ = syntax.HighlightWithoutTrailingNewline(w, line, language, theme.ChromaStyle)
		},
		func(line string) { printMarkdownLine(w, line, theme, false) },
		func(header []string, align []tableAlignment, rows []string) {
			renderBatchMarkdownTable(w, header, align, rows, theme, false)
		},
	)
}

// RenderRawReasoningContent renders raw reasoning/thinking text in muted italic
// without Markdown interpretation.
func RenderRawReasoningContent(w io.Writer, content string, theme style.UITheme) {
	dimStyle := GetThinkingStyle(theme)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line != "" {
			fmt.Fprint(w, dimStyle.Render(line))
		}
		if i < len(lines)-1 {
			fmt.Fprintln(w)
		}
	}
}

// RenderReasoningMarkdownContent renders reasoning/thinking content as subdued Markdown.
func RenderReasoningMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	codeStyle := style.NewStyle().Foreground(thinkingAccentColor(theme))
	renderMarkdownBlocks(
		w,
		content,
		func(line, _ string) { fmt.Fprint(w, codeStyle.Render("  "+line)) },
		func(line string) { printMarkdownLine(w, line, theme, true) },
		func(header []string, align []tableAlignment, rows []string) {
			renderBatchMarkdownTable(w, header, align, rows, theme, true)
		},
	)
}

func renderBatchMarkdownTable(w io.Writer, header []string, align []tableAlignment, rows []string, theme style.UITheme, thinking bool) {
	table := newMarkdownTable(w, header, align, theme, thinking)
	parsed := make([][]string, len(rows))
	for i, row := range rows {
		parsed[i], _ = markdownTableRow(row)
	}
	table.measureRows(parsed)
	for _, cells := range parsed {
		table.writeRow(w, cells)
	}
	table.finish(w)
}
