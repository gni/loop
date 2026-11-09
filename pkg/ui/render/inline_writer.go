package render

import (
	"io"
	"strings"
	"unicode"

	"loop/pkg/ui/style"
)

// InlineMarkdownWriter processes inline spans incrementally. The same parser
// is used for live output and history redraw, so completed spans have the same
// styling regardless of terminal resize or provider chunk boundaries.
type InlineMarkdownWriter struct {
	w                    io.Writer
	theme                style.UITheme
	block                MarkdownBlock
	headingLevel         int
	thinking             bool
	bold, italic, strike bool
	codeTicks            int
	escape               bool
	delimiter            rune
	delimiterCount       int
	previous             rune
	plain                strings.Builder
}

func NewInlineMarkdownWriter(w io.Writer, theme style.UITheme, block MarkdownBlock, headingLevel int, thinking bool) *InlineMarkdownWriter {
	return &InlineMarkdownWriter{w: w, theme: theme, block: block, headingLevel: headingLevel, thinking: thinking}
}

func (r *InlineMarkdownWriter) SetBlock(block MarkdownBlock, level int) {
	r.EndLine()
	r.block = block
	r.headingLevel = level
}

func (r *InlineMarkdownWriter) Write(text string) {
	for _, ch := range text {
		r.WriteRune(ch)
	}
	r.Flush()
}

func (r *InlineMarkdownWriter) WriteRune(ch rune) {
	if r.delimiter != 0 && ch == r.delimiter && !r.escape {
		r.delimiterCount++
		return
	}
	r.resolveDelimiter(ch)
	if r.escape {
		if !strings.ContainsRune("\\`*_~{}[]()#+-.!>|", ch) {
			r.plain.WriteRune('\\')
		}
		r.plain.WriteRune(ch)
		r.previous = ch
		r.escape = false
		return
	}
	if ch == '\\' && r.codeTicks == 0 {
		r.escape = true
		return
	}
	if ch == '*' || ch == '_' || ch == '~' || ch == '`' {
		r.delimiter = ch
		r.delimiterCount = 1
		return
	}
	r.plain.WriteRune(ch)
	r.previous = ch
}

func (r *InlineMarkdownWriter) resolveDelimiter(next rune) {
	if r.delimiter == 0 {
		return
	}
	marker, count := r.delimiter, r.delimiterCount
	r.delimiter, r.delimiterCount = 0, 0
	// Underscores inside identifiers are literal, not emphasis markers.
	if marker == '_' && (unicode.IsLetter(r.previous) || unicode.IsDigit(r.previous)) &&
		(unicode.IsLetter(next) || unicode.IsDigit(next)) {
		r.plain.WriteString(strings.Repeat(string(marker), count))
		r.previous = marker
		return
	}
	if r.codeTicks != 0 {
		if marker == '`' && count == r.codeTicks {
			r.Flush()
			r.codeTicks = 0
		} else {
			r.plain.WriteString(strings.Repeat(string(marker), count))
		}
		return
	}
	switch marker {
	case '`':
		r.Flush()
		r.codeTicks = count
	case '~':
		for count >= 2 {
			r.Flush()
			r.strike = !r.strike
			count -= 2
		}
		if count != 0 {
			r.plain.WriteRune('~')
		}
	case '*', '_':
		for count >= 3 {
			r.Flush()
			r.bold = !r.bold
			r.italic = !r.italic
			count -= 3
		}
		if count >= 2 {
			r.Flush()
			r.bold = !r.bold
			count -= 2
		}
		if count != 0 {
			r.Flush()
			r.italic = !r.italic
		}
	}
}

// Flush writes the current text without finalizing an unfinished delimiter.
func (r *InlineMarkdownWriter) Flush() {
	if r.plain.Len() == 0 {
		return
	}
	current := MarkdownStyle(r.theme, r.block, r.headingLevel, r.bold, r.italic, r.strike, r.codeTicks > 0)
	if r.thinking {
		current = style.NewStyle().Foreground(r.theme.Border).Italic(true)
		if r.bold {
			current = current.Bold(true)
		}
		if r.codeTicks > 0 {
			current = current.Underline(true)
		}
		if r.strike {
			current = current.Strikethrough(true)
		}
	}
	_, _ = io.WriteString(r.w, current.Render(r.plain.String()))
	r.plain.Reset()
}

func (r *InlineMarkdownWriter) EndLine() {
	if r.delimiter != 0 && !r.bold && !r.italic && !r.strike && r.codeTicks == 0 {
		r.plain.WriteString(strings.Repeat(string(r.delimiter), r.delimiterCount))
		r.delimiter, r.delimiterCount = 0, 0
	} else {
		r.resolveDelimiter(0)
	}
	if r.escape {
		r.plain.WriteRune('\\')
	}
	r.Flush()
	r.Reset()
}

func (r *InlineMarkdownWriter) Reset() {
	r.bold, r.italic, r.strike = false, false, false
	r.codeTicks = 0
	r.delimiter, r.delimiterCount = 0, 0
	r.escape = false
	r.previous = 0
	r.plain.Reset()
	r.block = MarkdownBody
	r.headingLevel = 0
}
