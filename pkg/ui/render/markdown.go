package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/terminal"
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

// WrapMarkdownLine wraps a single markdown line respecting list and blockquote prefixes.
func WrapMarkdownLine(line string, width int) []string {
	if width <= 10 {
		return []string{line}
	}
	trimmed := strings.TrimSpace(line)
	var prefix string
	var content string

	if strings.HasPrefix(trimmed, ">") {
		prefix = "> "
		content = strings.TrimSpace(trimmed[1:])
	} else if strings.HasPrefix(trimmed, "- ") {
		prefix = "- "
		content = trimmed[2:]
	} else if strings.HasPrefix(trimmed, "* ") {
		prefix = "* "
		content = trimmed[2:]
	} else if strings.HasPrefix(trimmed, "• ") {
		prefix = "• "
		content = trimmed[2:]
	} else if ok, numPrefix, listText := IsNumberedList(trimmed); ok {
		prefix = numPrefix + " "
		content = listText
	} else {
		content = line
	}

	var leadingSpaces string
	if prefix == "" {
		for _, r := range line {
			if r == ' ' {
				leadingSpaces += " "
			} else {
				break
			}
		}
	}

	words := strings.Split(content, " ")
	var lines []string
	var currentLine strings.Builder

	effectiveWidth := width - len(prefix) - len(leadingSpaces)
	if effectiveWidth < 15 {
		effectiveWidth = 15
	}

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= effectiveWidth {
			currentLine.WriteByte(' ')
			currentLine.WriteString(word)
		} else {
			lines = append(lines, prefix+leadingSpaces+currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, prefix+leadingSpaces+currentLine.String())
	}
	if len(lines) == 0 {
		lines = append(lines, line)
	}
	return lines
}

// RenderInlineMarkdown parses and renders inline code, bold, and italic markers.
func RenderInlineMarkdown(text string, inThinking bool, theme style.UITheme) string {
	var result strings.Builder
	runes := []rune(text)
	n := len(runes)

	for i := 0; i < n; {
		// 1. Inline code: `code`
		if runes[i] == '`' {
			j := i + 1
			for j < n && runes[j] != '`' {
				j++
			}
			if j < n {
				codeVal := string(runes[i+1 : j])
				var styled string
				if inThinking {
					styled = style.NewStyle().Foreground(theme.Border).Underline(true).Italic(true).Render(codeVal)
				} else {
					styled = style.NewStyle().Foreground(theme.Highlight).Render(codeVal)
				}
				result.WriteString(styled)
				i = j + 1
				continue
			}
		}

		// 2. Bold: **bold**
		if i+1 < n && runes[i] == '*' && runes[i+1] == '*' {
			j := i + 2
			found := false
			for j+1 < n {
				if runes[j] == '*' && runes[j+1] == '*' {
					found = true
					break
				}
				j++
			}
			if found {
				boldVal := string(runes[i+2 : j])
				var styled string
				if inThinking {
					styled = style.NewStyle().Foreground(theme.Border).Bold(true).Italic(true).Render(RenderInlineMarkdown(boldVal, inThinking, theme))
				} else {
					styled = style.NewStyle().Foreground(theme.Primary).Bold(true).Render(RenderInlineMarkdown(boldVal, inThinking, theme))
				}
				result.WriteString(styled)
				i = j + 2
				continue
			}
		}

		// 3. Italic: *italic*
		if runes[i] == '*' {
			j := i + 1
			for j < n && runes[j] != '*' {
				j++
			}
			if j < n {
				italicVal := string(runes[i+1 : j])
				var styled string
				if inThinking {
					styled = style.NewStyle().Foreground(theme.Border).Italic(true).Render(RenderInlineMarkdown(italicVal, inThinking, theme))
				} else {
					styled = style.NewStyle().Italic(true).Render(RenderInlineMarkdown(italicVal, inThinking, theme))
				}
				result.WriteString(styled)
				i = j + 1
				continue
			}
		}

		result.WriteRune(runes[i])
		i++
	}

	return result.String()
}

// PrintNormalMarkdownLine renders a single line with markdown blocks (headers, quotes, bullets, lists).
func PrintNormalMarkdownLine(w io.Writer, line string, theme style.UITheme) {
	trimmed := strings.TrimSpace(line)

	// 0. Handle delimiter: make a space instead of "----"
	if trimmed == "----" {
		fmt.Fprint(w, " ")
		return
	}

	// 1. Handle headers: e.g. "# Header", "## Header", etc.
	if strings.HasPrefix(trimmed, "#") {
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		headerText := strings.TrimSpace(trimmed[hashes:])
		var styled string
		if hashes == 1 {
			styled = style.NewStyle().Foreground(theme.Secondary).Bold(true).Underline(true).Render(headerText)
		} else {
			styled = style.NewStyle().Foreground(theme.Secondary).Bold(true).Render(headerText)
		}
		fmt.Fprint(w, styled)
		return
	}

	// 2. Handle blockquotes: e.g. "> text"
	if strings.HasPrefix(trimmed, ">") {
		quoteText := strings.TrimSpace(trimmed[1:])
		styled := style.NewStyle().Foreground(theme.Border).Italic(true).Render("┃ " + quoteText)
		fmt.Fprint(w, styled)
		return
	}

	// 3. Handle bullet points: e.g. "- item" or "* item"
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "• ") {
		bulletText := trimmed[2:]
		bulletSymbol := style.NewStyle().Foreground(theme.Primary).Render("•")
		fmt.Fprintf(w, "  %s %s", bulletSymbol, RenderInlineMarkdown(bulletText, false, theme))
		return
	}

	// 3.5 Handle numbered lists: e.g. "1. item"
	if ok, numPrefix, listText := IsNumberedList(trimmed); ok {
		numSymbol := style.NewStyle().Foreground(theme.Primary).Render(numPrefix)
		fmt.Fprintf(w, "  %s %s", numSymbol, RenderInlineMarkdown(listText, false, theme))
		return
	}

	// 4. Standard text line: parse inline styles (bold, code, italic)
	fmt.Fprint(w, RenderInlineMarkdown(line, false, theme))
}

// RenderMarkdownContent renders multiline markdown including code blocks and wrapped lines.
func RenderMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	for i, line := range lines {
		if inCodeBlock {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inCodeBlock = false
			} else {
				fmt.Fprint(w, style.NewStyle().Foreground(theme.Highlight).Render(line))
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		} else {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inCodeBlock = true
			} else {
				termW, _ := terminal.GetDimensions()
				wrapLimit := termW - 5
				if wrapLimit < 20 {
					wrapLimit = 20
				}
				wrapped := WrapMarkdownLine(line, wrapLimit)
				for idx, wl := range wrapped {
					PrintNormalMarkdownLine(w, wl, theme)
					if idx < len(wrapped)-1 {
						fmt.Fprintln(w)
					}
				}
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		}
	}
}

// RenderReasoningMarkdownContent renders reasoning/thinking markdown content.
func RenderReasoningMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	dimStyle := GetThinkingStyle(theme)
	for i, line := range lines {
		if inCodeBlock {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inCodeBlock = false
			} else {
				fmt.Fprint(w, dimStyle.Render("  "+line))
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		} else {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inCodeBlock = true
			} else {
				termW, _ := terminal.GetDimensions()
				wrapLimit := termW - 5
				if wrapLimit < 20 {
					wrapLimit = 20
				}
				wrapped := WrapMarkdownLine(line, wrapLimit)
				for idx, wl := range wrapped {
					fmt.Fprint(w, dimStyle.Render(wl))
					if idx < len(wrapped)-1 {
						fmt.Fprintln(w)
					}
				}
				if i < len(lines)-1 {
					fmt.Fprintln(w)
				}
			}
		}
	}
}
