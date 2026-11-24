package markdown

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/ui/style"
)

const (
	maxInlineMathBytes  = 4 * 1024
	maxDisplayMathBytes = 32 * 1024
)

type mathBlockKind uint8

const (
	mathBlockNone    mathBlockKind = iota
	mathBlockDollar                // $$ ... $$
	mathBlockBracket               // \[ ... \]
	mathBlockEnv                   // \begin{aligned} ... \end{aligned}
)

// MathStyle returns the calm theme-aware style used for rendered LaTeX spans.
func MathStyle(theme style.UITheme, block MarkdownBlock, headingLevel int, bold, italic, strike, thinking bool) style.Style {
	if thinking {
		s := style.NewStyle().Foreground(thinkingAccentColor(theme))
		if bold {
			s = s.Bold(true)
		}
		if strike {
			s = s.Strikethrough(true)
		}
		return s
	}
	fg := theme.Primary
	if theme.Text != nil && theme.Primary != nil {
		fg = style.BlendColor(theme.Text, theme.Primary, 0.55)
	}
	if block == MarkdownHeading && headingLevel <= 2 {
		fg = theme.Primary
	}
	s := style.NewStyle().Foreground(fg)
	if bold || block == MarkdownHeading {
		s = s.Bold(true)
	}
	if italic {
		s = s.Italic(true)
	}
	if strike {
		s = s.Strikethrough(true)
	}
	return s
}

func renderDisplayMathBlock(w io.Writer, body string, theme style.UITheme, thinking bool) {
	maxW := max(20, markdownWidth(w)-2)
	lines := renderLatexDisplay(body, maxW)
	if len(lines) == 0 {
		return
	}
	st := MathStyle(theme, MarkdownBody, 0, false, false, false, thinking)
	for i, line := range lines {
		if i > 0 {
			fmt.Fprint(w, "\n")
		}
		if line != "" {
			fmt.Fprint(w, "  ", st.Render(line))
		}
	}
}

// parseOpeningMathBlock checks whether line opens a display LaTeX block
// ($$, \[, or standalone \begin{aligned|align|equation|gather|cases|pmatrix|...}).
// When closed is true, the entire block was contained on a single line.
func parseOpeningMathBlock(line string) (kind mathBlockKind, env string, body string, closed bool, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return mathBlockNone, "", "", false, false
	}
	trimmed = strings.TrimRight(trimmed, " \t\r")

	if strings.HasPrefix(trimmed, "$$") {
		rest := trimmed[2:]
		if end := findClosingDollarPair(rest); end >= 0 {
			after := strings.TrimSpace(rest[end+2:])
			if after == "" {
				return mathBlockDollar, "", strings.TrimSpace(rest[:end]), true, true
			}
			// Text follows closing $$ on the same line; let inline math handle it.
			return mathBlockNone, "", "", false, false
		}
		return mathBlockDollar, "", rest, false, true
	}

	if strings.HasPrefix(trimmed, `\[`) {
		rest := trimmed[2:]
		if end := strings.Index(rest, `\]`); end >= 0 {
			after := strings.TrimSpace(rest[end+2:])
			if after == "" {
				return mathBlockBracket, "", strings.TrimSpace(rest[:end]), true, true
			}
			return mathBlockNone, "", "", false, false
		}
		return mathBlockBracket, "", rest, false, true
	}

	if strings.HasPrefix(trimmed, `\begin{`) {
		closeBrace := strings.IndexByte(trimmed[7:], '}')
		if closeBrace > 0 {
			envName := strings.TrimSpace(trimmed[7 : 7+closeBrace])
			if latexDisplayEnvs[envName] {
				endTag := `\end{` + envName + `}`
				if endIdx := strings.Index(trimmed, endTag); endIdx >= 0 {
					after := strings.TrimSpace(trimmed[endIdx+len(endTag):])
					if after == "" {
						return mathBlockEnv, envName, trimmed[:endIdx+len(endTag)], true, true
					}
					return mathBlockNone, "", "", false, false
				}
				return mathBlockEnv, envName, trimmed, false, true
			}
		}
	}

	return mathBlockNone, "", "", false, false
}

// isClosingMathBlock reports whether line terminates an open display math block
// and returns any math body fragment preceding the closing delimiter on that line.
func isClosingMathBlock(line string, kind mathBlockKind, env string) (prefix string, closed bool) {
	trimmed := strings.TrimRight(line, " \t\r")
	switch kind {
	case mathBlockDollar:
		if idx := findClosingDollarPair(trimmed); idx >= 0 && strings.TrimSpace(trimmed[idx+2:]) == "" {
			return trimmed[:idx], true
		}
	case mathBlockBracket:
		if idx := strings.Index(trimmed, `\]`); idx >= 0 && strings.TrimSpace(trimmed[idx+2:]) == "" {
			return trimmed[:idx], true
		}
	case mathBlockEnv:
		endTag := `\end{` + env + `}`
		if idx := strings.Index(trimmed, endTag); idx >= 0 && strings.TrimSpace(trimmed[idx+len(endTag):]) == "" {
			return trimmed[:idx+len(endTag)], true
		}
	}
	return "", false
}

func findClosingDollarPair(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '$' && s[i+1] == '$' {
			return i
		}
	}
	return -1
}
