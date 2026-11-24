package markdown

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

type inlineMathKind uint8

const (
	inlineMathNone inlineMathKind = iota
	inlineMathDollarPending
	inlineMathDollar
	inlineMathDoubleDollar
	inlineMathParen
	inlineMathBracket
)

// InlineMarkdownWriter processes inline spans incrementally, including inline
// LaTeX math ($...$, $$...$$, and \(...\)). The same parser is used for live
// output, table cells, and history redraw so completed spans match identically.
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

	mathKind         inlineMathKind
	mathBuf          strings.Builder
	mathEscape       bool
	mathClosePending bool
	mathDisabled     bool

	inEntity  bool
	entityBuf strings.Builder
}

func NewInlineMarkdownWriter(w io.Writer, theme style.UITheme, block MarkdownBlock, headingLevel int, thinking bool) *InlineMarkdownWriter {
	return &InlineMarkdownWriter{w: w, theme: theme, block: block, headingLevel: headingLevel, thinking: thinking}
}

func (r *InlineMarkdownWriter) SetWriter(w io.Writer) {
	r.w = w
}

func (r *InlineMarkdownWriter) SetTheme(theme style.UITheme) {
	r.theme = theme
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
	if r.inEntity {
		if r.writeEntityRune(ch) {
			return
		}
	}
	if !r.mathDisabled && r.mathKind != inlineMathNone {
		if r.writeMathRune(ch) {
			return
		}
	}
	if r.delimiter != 0 && ch == r.delimiter && !r.escape {
		r.delimiterCount++
		return
	}
	r.resolveDelimiter(ch)
	if r.escape {
		if (ch == '(' || ch == '[') && r.codeTicks == 0 && !r.mathDisabled {
			r.escape = false
			if ch == '(' {
				r.mathKind = inlineMathParen
			} else {
				r.mathKind = inlineMathBracket
			}
			r.mathBuf.Reset()
			r.mathEscape = false
			r.mathClosePending = false
			return
		}
		if !strings.ContainsRune("\\`*_~{}[]()#+-.!>|$", ch) {
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
	if ch == '$' && r.codeTicks == 0 && !r.mathDisabled {
		r.mathKind = inlineMathDollarPending
		r.mathBuf.Reset()
		r.mathEscape = false
		r.mathClosePending = false
		return
	}
	if ch == '&' && r.codeTicks == 0 {
		r.inEntity = true
		r.entityBuf.Reset()
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

// writeMathRune processes a rune inside an active inline math span.
// Returns true if ch was consumed, or false if ch should be re-processed normally.
func (r *InlineMarkdownWriter) writeMathRune(ch rune) bool {
	switch r.mathKind {
	case inlineMathDollarPending:
		if ch == '$' {
			r.mathKind = inlineMathDoubleDollar
			return true
		}
		if unicode.IsSpace(ch) {
			// "$ " is never an inline math opener (e.g. "$ 5").
			r.mathKind = inlineMathNone
			r.plain.WriteByte('$')
			r.previous = '$'
			return false
		}
		r.mathKind = inlineMathDollar
		if ch == '\\' {
			r.mathBuf.WriteRune('\\')
			r.mathEscape = true
		} else {
			r.mathBuf.WriteRune(ch)
		}
		return true

	case inlineMathDollar:
		if r.mathClosePending {
			r.mathClosePending = false
			if unicode.IsDigit(ch) {
				// Closing '$' followed by digit (e.g. "$5 and $10") is currency.
				r.mathBuf.WriteByte('$')
				r.mathBuf.WriteRune(ch)
				return true
			}
			if ch == '$' {
				// "$/$$" style notation: a single '$' cannot close right into '$$'.
				raw := "$" + r.mathBuf.String() + "$$"
				r.resetMath()
				r.replayPlainLiteral(raw)
				return true
			}
			// Valid closing '$': render math, then process ch normally.
			content := r.mathBuf.String()
			r.resetMath()
			r.emitRenderedMath(content)
			return false
		}
		if r.mathEscape {
			r.mathBuf.WriteRune(ch)
			r.mathEscape = false
			return true
		}
		if ch == '\\' {
			r.mathBuf.WriteRune('\\')
			r.mathEscape = true
			return true
		}
		if ch == '$' {
			content := r.mathBuf.String()
			last, _ := utf8.DecodeLastRuneInString(content)
			if len(content) == 0 || unicode.IsSpace(last) {
				// Trailing whitespace before '$' invalidates the earlier opener
				// (e.g. "costs $5, so $x^2$"). Replay earlier text literally and
				// let this '$' start a fresh math candidate.
				r.resetMath()
				r.replayPlainLiteral("$" + content)
				r.mathKind = inlineMathDollarPending
				return true
			}
			r.mathClosePending = true
			return true
		}
		r.mathBuf.WriteRune(ch)
		if r.mathBuf.Len() > maxInlineMathBytes {
			raw := "$" + r.mathBuf.String()
			r.resetMath()
			r.replayPlainLiteral(raw)
		}
		return true

	case inlineMathDoubleDollar:
		if r.mathClosePending {
			r.mathClosePending = false
			if ch == '$' {
				content := r.mathBuf.String()
				r.resetMath()
				if strings.TrimSpace(content) == "" {
					r.plain.WriteString("$$" + content + "$$")
					r.previous = '$'
				} else {
					r.emitRenderedMath(content)
				}
				return true
			}
			r.mathBuf.WriteByte('$')
			r.mathBuf.WriteRune(ch)
			return true
		}
		if r.mathEscape {
			r.mathBuf.WriteRune(ch)
			r.mathEscape = false
			return true
		}
		if ch == '\\' {
			r.mathBuf.WriteRune('\\')
			r.mathEscape = true
			return true
		}
		if ch == '$' {
			r.mathClosePending = true
			return true
		}
		r.mathBuf.WriteRune(ch)
		if r.mathBuf.Len() > maxInlineMathBytes {
			raw := "$$" + r.mathBuf.String()
			r.resetMath()
			r.replayPlainLiteral(raw)
		}
		return true

	case inlineMathParen, inlineMathBracket:
		closeCh := ')'
		openPrefix := `\(`
		closeSuffix := `\)`
		if r.mathKind == inlineMathBracket {
			closeCh = ']'
			openPrefix = `\[`
			closeSuffix = `\]`
		}
		if r.mathEscape {
			r.mathEscape = false
			if ch == closeCh {
				content := r.mathBuf.String()
				if strings.HasSuffix(content, `\`) {
					content = content[:len(content)-1]
				}
				r.resetMath()
				if strings.TrimSpace(content) == "" {
					r.plain.WriteString(openPrefix + content + closeSuffix)
					r.previous = closeCh
				} else {
					r.emitRenderedMath(content)
				}
				return true
			}
			r.mathBuf.WriteRune(ch)
			return true
		}
		if ch == '\\' {
			r.mathBuf.WriteRune('\\')
			r.mathEscape = true
			return true
		}
		r.mathBuf.WriteRune(ch)
		if r.mathBuf.Len() > maxInlineMathBytes {
			raw := openPrefix + r.mathBuf.String()
			r.resetMath()
			r.replayPlainLiteral(raw)
		}
		return true
	}
	return false
}

func (r *InlineMarkdownWriter) writeEntityRune(ch rune) bool {
	if ch == ';' {
		name := r.entityBuf.String()
		r.inEntity = false
		r.entityBuf.Reset()
		switch name {
		case "nbsp", "ensp", "thinsp":
			r.plain.WriteByte(' ')
			r.previous = ' '
			return true
		case "emsp":
			r.plain.WriteString("  ")
			r.previous = ' '
			return true
		}
		r.plain.WriteByte('&')
		r.plain.WriteString(name)
		r.plain.WriteByte(';')
		r.previous = ';'
		return true
	}
	if ((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')) && r.entityBuf.Len() < 10 {
		r.entityBuf.WriteRune(ch)
		return true
	}
	raw := "&" + r.entityBuf.String()
	r.inEntity = false
	r.entityBuf.Reset()
	r.plain.WriteString(raw)
	if last, _ := utf8.DecodeLastRuneInString(raw); last != utf8.RuneError {
		r.previous = last
	}
	return false
}

func (r *InlineMarkdownWriter) resetMath() {
	r.mathKind = inlineMathNone
	r.mathBuf.Reset()
	r.mathEscape = false
	r.mathClosePending = false
}

func (r *InlineMarkdownWriter) emitRenderedMath(src string) {
	rendered := renderLatexInline(src)
	if rendered == "" {
		return
	}
	r.Flush()
	st := MathStyle(r.theme, r.block, r.headingLevel, r.bold, r.italic, r.strike, r.thinking)
	_, _ = io.WriteString(r.w, st.Render(rendered))
	last, _ := utf8.DecodeLastRuneInString(rendered)
	r.previous = last
}

func (r *InlineMarkdownWriter) replayPlainLiteral(raw string) {
	prevDisabled := r.mathDisabled
	r.mathDisabled = true
	for _, ch := range raw {
		r.WriteRune(ch)
	}
	r.mathDisabled = prevDisabled
}

func (r *InlineMarkdownWriter) replayUnclosedMath(opener, tail string) {
	r.plain.WriteString(opener)
	if len(opener) > 0 {
		last, _ := utf8.DecodeLastRuneInString(opener)
		r.previous = last
	}
	for _, ch := range tail {
		r.WriteRune(ch)
	}
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
	var current style.Style
	if r.thinking {
		current = ThinkingMarkdownStyle(r.theme, r.block, r.headingLevel, r.bold, r.italic, r.strike, r.codeTicks > 0)
	} else {
		current = MarkdownStyle(r.theme, r.block, r.headingLevel, r.bold, r.italic, r.strike, r.codeTicks > 0)
	}
	_, _ = io.WriteString(r.w, current.Render(r.plain.String()))
	r.plain.Reset()
}

func (r *InlineMarkdownWriter) EndLine() {
	if r.inEntity {
		raw := "&" + r.entityBuf.String()
		r.inEntity = false
		r.entityBuf.Reset()
		r.plain.WriteString(raw)
		if last, _ := utf8.DecodeLastRuneInString(raw); last != utf8.RuneError {
			r.previous = last
		}
	}
	for steps := 0; r.mathKind != inlineMathNone; steps++ {
		switch r.mathKind {
		case inlineMathDollar:
			if r.mathClosePending {
				content := r.mathBuf.String()
				r.resetMath()
				r.emitRenderedMath(content)
				continue
			}
			tail := r.mathBuf.String()
			r.resetMath()
			if steps >= 8 {
				r.replayPlainLiteral("$" + tail)
			} else {
				r.replayUnclosedMath("$", tail)
			}
		case inlineMathDollarPending:
			r.resetMath()
			r.plain.WriteByte('$')
			r.previous = '$'
		case inlineMathDoubleDollar:
			tail := r.mathBuf.String()
			if r.mathClosePending {
				tail += "$"
			}
			r.resetMath()
			if steps >= 8 {
				r.replayPlainLiteral("$$" + tail)
			} else {
				r.replayUnclosedMath("$$", tail)
			}
		case inlineMathParen:
			tail := r.mathBuf.String()
			r.resetMath()
			if steps >= 8 {
				r.replayPlainLiteral(`\(` + tail)
			} else {
				r.replayUnclosedMath(`\(`, tail)
			}
		case inlineMathBracket:
			tail := r.mathBuf.String()
			r.resetMath()
			if steps >= 8 {
				r.replayPlainLiteral(`\[` + tail)
			} else {
				r.replayUnclosedMath(`\[`, tail)
			}
		}
	}
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
	r.inEntity = false
	r.entityBuf.Reset()
	r.resetMath()
	r.block = MarkdownBody
	r.headingLevel = 0
}
