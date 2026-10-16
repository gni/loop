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

func extractBulletPrefix(trimmed string) (string, string, bool) {
	for _, p := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(trimmed, p) {
			return p, trimmed[len(p):], true
		}
	}
	return "", "", false
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
	} else if bulletPrefix, rest, ok := extractBulletPrefix(trimmed); ok {
		prefix = bulletPrefix
		content = rest
	} else if ok, numPrefix, listText := IsNumberedList(trimmed); ok {
		prefix = numPrefix + " "
		content = listText
	} else {
		content = line
	}

	var leadingSpaces string
	if prefix == "" {
		leadingLen := len(line) - len(strings.TrimLeft(line, " "))
		if leadingLen > 0 {
			leadingSpaces = line[:leadingLen]
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
				inner := RenderInlineMarkdown(boldVal, inThinking, theme)
				var styled string
				if inThinking {
					styled = style.NewStyle().Foreground(theme.Border).Bold(true).Italic(true).Render(inner)
				} else {
					styled = style.NewStyle().Foreground(theme.Primary).Bold(true).Render(inner)
				}
				result.WriteString(styled)
				i = j + 2
				continue
			}
		}

		if runes[i] == '*' {
			j := i + 1
			for j < n && runes[j] != '*' {
				j++
			}
			if j < n {
				italicVal := string(runes[i+1 : j])
				inner := RenderInlineMarkdown(italicVal, inThinking, theme)
				var styled string
				if inThinking {
					styled = style.NewStyle().Foreground(theme.Border).Italic(true).Render(inner)
				} else {
					styled = style.NewStyle().Italic(true).Render(inner)
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

	if trimmed == "----" {
		fmt.Fprint(w, " ")
		return
	}

	if strings.HasPrefix(trimmed, "#") {
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		headerText := strings.TrimSpace(trimmed[hashes:])
		headerStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		if hashes == 1 {
			headerStyle = headerStyle.Underline(true)
		}
		fmt.Fprint(w, headerStyle.Render(headerText))
		return
	}

	if strings.HasPrefix(trimmed, ">") {
		quoteText := strings.TrimSpace(trimmed[1:])
		styled := style.NewStyle().Foreground(theme.Border).Italic(true).Render("┃ " + quoteText)
		fmt.Fprint(w, styled)
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

func renderMarkdownBlocks(w io.Writer, content string, onCodeLine func(string), onTextLine func(string)) {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			onCodeLine(line)
		} else {
			termW, _ := terminal.GetDimensions()
			wrapLimit := termW - 5
			if wrapLimit < 20 {
				wrapLimit = 20
			}
			wrapped := WrapMarkdownLine(line, wrapLimit)
			for idx, wl := range wrapped {
				onTextLine(wl)
				if idx < len(wrapped)-1 {
					fmt.Fprintln(w)
				}
			}
		}
		if i < len(lines)-1 {
			fmt.Fprintln(w)
		}
	}
}

// RenderMarkdownContent renders multiline markdown including code blocks and wrapped lines.
func RenderMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	codeStyle := style.NewStyle().Foreground(theme.Highlight)
	renderMarkdownBlocks(
		w,
		content,
		func(line string) { fmt.Fprint(w, codeStyle.Render(line)) },
		func(line string) { PrintNormalMarkdownLine(w, line, theme) },
	)
}

// RenderReasoningMarkdownContent renders reasoning/thinking markdown content.
func RenderReasoningMarkdownContent(w io.Writer, content string, theme style.UITheme) {
	dimStyle := GetThinkingStyle(theme)
	renderMarkdownBlocks(
		w,
		content,
		func(line string) { fmt.Fprint(w, dimStyle.Render("  "+line)) },
		func(line string) { fmt.Fprint(w, dimStyle.Render(line)) },
	)
}