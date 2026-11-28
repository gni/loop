package markdown

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/render/syntax"
	"loop/pkg/ui/style"
)

const (
	maxLiveFenceHeaderBytes = 128
	maxLiveCodeLineBytes    = 256 * 1024
)

type livePrefixKind uint8

const (
	livePrefixPending livePrefixKind = iota
	livePrefixPlain
	livePrefixFence
	livePrefixHeading
	livePrefixQuote
	livePrefixBullet
	livePrefixNumbered
	livePrefixTable
	livePrefixRule
	livePrefixMathLine
)

type livePrefixDecision struct {
	kind         livePrefixKind
	headingLevel int
	fenceLen     int
	marker       string
	remainder    string
}

// LiveMarkdownRenderer incrementally styles streamed markdown text.
type LiveMarkdownRenderer struct {
	w        io.Writer
	theme    style.UITheme
	thinking bool

	atLineStart bool
	prefix      strings.Builder

	inFenceHeader bool
	fenceHeader   strings.Builder
	fenceLen      int
	inCodeBlock   bool
	codeLanguage  string
	codeLine      strings.Builder
	codeOverflow  bool

	inMathHeader bool
	mathHeader   strings.Builder
	inMathBlock  bool
	mathKind     mathBlockKind
	mathEnv      string
	mathLine     strings.Builder
	mathBuf      strings.Builder

	inline           *InlineMarkdownWriter
	table            liveMarkdownTable
	tablePassthrough bool
	utf8Pending      string

	endedWithNewline bool
}

// NewLiveMarkdownRenderer creates a new streaming markdown renderer.
func NewLiveMarkdownRenderer(w io.Writer, theme style.UITheme) *LiveMarkdownRenderer {
	return newLiveMarkdownRenderer(w, theme, false)
}

// NewLiveThinkingMarkdownRenderer creates a streaming markdown renderer styled
// for agent thinking/reasoning output.
func NewLiveThinkingMarkdownRenderer(w io.Writer, theme style.UITheme) *LiveMarkdownRenderer {
	return newLiveMarkdownRenderer(w, theme, true)
}

func newLiveMarkdownRenderer(w io.Writer, theme style.UITheme, thinking bool) *LiveMarkdownRenderer {
	return &LiveMarkdownRenderer{
		w:                w,
		theme:            theme,
		thinking:         thinking,
		atLineStart:      true,
		endedWithNewline: true,
		inline:           NewInlineMarkdownWriter(w, theme, MarkdownBody, 0, thinking),
	}
}

// SetWriter replaces the destination writer while keeping incremental parser state.
func (r *LiveMarkdownRenderer) SetWriter(w io.Writer) {
	r.w = w
	r.inline.SetWriter(w)
}

// SetTheme updates the active color palette for subsequent streamed markdown output.
func (r *LiveMarkdownRenderer) SetTheme(theme style.UITheme) {
	r.theme = theme
	if r.inline != nil {
		r.inline.SetTheme(theme)
	}
	if r.table.active != nil {
		r.table.active.theme = theme
	}
}

func (r *LiveMarkdownRenderer) Write(chunk string) {
	var plain strings.Builder
	chunk = r.utf8Pending + chunk
	r.utf8Pending = ""
	for len(chunk) > 0 {
		if !utf8.FullRuneInString(chunk) {
			r.utf8Pending = chunk
			break
		}
		current, size := utf8.DecodeRuneInString(chunk)
		r.writeRune(current, &plain)
		chunk = chunk[size:]
	}
	r.flushPlain(&plain)
	r.inline.Flush()
}

func (r *LiveMarkdownRenderer) writeRune(current rune, plain *strings.Builder) {
	if r.writeTableRune(current, plain) {
		return
	}
	r.writeMarkdownRune(current, plain)
}

func (r *LiveMarkdownRenderer) writeMarkdownRune(current rune, plain *strings.Builder) {
	if r.inMathHeader {
		r.writeMathHeaderRune(current, plain)
		return
	}
	if r.inMathBlock {
		r.writeMathBlockRune(current, plain)
		return
	}
	if r.inFenceHeader {
		r.writeFenceHeaderRune(current, plain)
		return
	}
	if r.inCodeBlock {
		r.writeCodeRune(current, plain)
		return
	}
	if r.atLineStart {
		r.prefix.WriteRune(current)
		decision := classifyLivePrefix(r.prefix.String())
		if decision.kind == livePrefixTable && r.tablePassthrough {
			decision.kind = livePrefixPlain
		}
		switch decision.kind {
		case livePrefixPending:
			return
		case livePrefixFence:
			r.flushPlain(plain)
			r.inline.EndLine()
			rawPrefix := r.prefix.String()
			r.prefix.Reset()
			r.prefix.WriteString(rawPrefix[:len(rawPrefix)-len(decision.remainder)])
			r.fenceLen = decision.fenceLen
			r.inFenceHeader = true
			r.atLineStart = false
			for _, rem := range decision.remainder {
				if r.inFenceHeader {
					r.writeFenceHeaderRune(rem, plain)
				} else {
					r.writeInlineRune(rem, plain)
				}
			}
			return
		case livePrefixRule:
			r.prefix.Reset()
			r.atLineStart = true
			r.flushPlain(plain)
			r.inline.EndLine()
			r.emitVisible(" \n")
			return
		case livePrefixHeading:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			r.inline.SetBlock(MarkdownHeading, decision.headingLevel)
			return
		case livePrefixQuote:
			r.prefix.Reset()
			r.atLineStart = false
			markerColor := r.theme.Border
			if r.thinking {
				markerColor = thinkingBaseColor(r.theme)
			}
			r.emitStyledMarker("┃ ", markerColor, plain)
			r.inline.SetBlock(MarkdownQuote, 0)
			for _, rem := range decision.remainder {
				r.writeInlineRune(rem, plain)
			}
			return
		case livePrefixBullet:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			markerColor := r.theme.Primary
			if r.thinking {
				markerColor = thinkingAccentColor(r.theme)
			}
			fmt.Fprintf(r.w, "  %s ", style.NewStyle().Foreground(markerColor).Render("•"))
			r.endedWithNewline = false
			return
		case livePrefixNumbered:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			markerColor := r.theme.Primary
			if r.thinking {
				markerColor = thinkingAccentColor(r.theme)
			}
			number := style.NewStyle().Foreground(markerColor).Render(decision.marker)
			fmt.Fprintf(r.w, "  %s ", number)
			r.endedWithNewline = false
			return
		case livePrefixTable:
			r.flushPlain(plain)
			r.inline.EndLine()
			r.table.line.WriteString(r.prefix.String())
			r.table.candidate = true
			r.prefix.Reset()
			r.atLineStart = false
			return
		case livePrefixMathLine:
			r.flushPlain(plain)
			r.inline.EndLine()
			r.mathHeader.Reset()
			r.mathHeader.WriteString(r.prefix.String())
			r.prefix.Reset()
			r.inMathHeader = true
			r.atLineStart = false
			if strings.HasSuffix(r.mathHeader.String(), "\n") {
				line := strings.TrimSuffix(r.mathHeader.String(), "\n")
				r.mathHeader.Reset()
				r.inMathHeader = false
				r.finishMathHeaderLine(line, true, plain)
			}
			return
		case livePrefixPlain:
			prefix := r.prefix.String()
			r.prefix.Reset()
			r.atLineStart = false
			for _, prefixRune := range prefix {
				r.writeInlineRune(prefixRune, plain)
			}
			return
		}
	}

	r.writeInlineRune(current, plain)
}

func (r *LiveMarkdownRenderer) writeMathHeaderRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		line := r.mathHeader.String()
		r.mathHeader.Reset()
		r.inMathHeader = false
		r.finishMathHeaderLine(line, true, plain)
		return
	}
	r.mathHeader.WriteRune(current)
	if r.mathHeader.Len() > maxDisplayMathBytes {
		line := r.mathHeader.String()
		r.mathHeader.Reset()
		r.inMathHeader = false
		r.replayInlineLine(line, false, plain)
	}
}

func (r *LiveMarkdownRenderer) finishMathHeaderLine(line string, hasNewline bool, plain *strings.Builder) {
	if kind, env, body, closed, ok := parseOpeningMathBlock(line); ok {
		if closed {
			r.flushPlain(plain)
			renderDisplayMathBlock(r.w, body, r.theme, r.thinking)
			r.endedWithNewline = false
			if hasNewline {
				r.emitVisible("\n")
			}
			r.atLineStart = hasNewline
			return
		}
		r.inMathBlock = true
		r.mathKind = kind
		r.mathEnv = env
		r.mathLine.Reset()
		r.mathBuf.Reset()
		if body != "" {
			r.mathBuf.WriteString(body)
		}
		r.atLineStart = true
		return
	}
	r.replayInlineLine(line, hasNewline, plain)
}

func (r *LiveMarkdownRenderer) replayInlineLine(line string, hasNewline bool, plain *strings.Builder) {
	for _, ch := range line {
		r.writeInlineRune(ch, plain)
	}
	if hasNewline {
		r.writeInlineRune('\n', plain)
	}
}

func (r *LiveMarkdownRenderer) writeMathBlockRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		line := r.mathLine.String()
		r.mathLine.Reset()
		if prefix, closed := isClosingMathBlock(line, r.mathKind, r.mathEnv); closed {
			if prefix != "" {
				if r.mathBuf.Len() > 0 {
					r.mathBuf.WriteByte('\n')
				}
				r.mathBuf.WriteString(prefix)
			}
			r.flushPlain(plain)
			renderDisplayMathBlock(r.w, r.mathBuf.String(), r.theme, r.thinking)
			r.endedWithNewline = false
			r.emitVisible("\n")
			r.inMathBlock = false
			r.mathKind = mathBlockNone
			r.mathEnv = ""
			r.mathBuf.Reset()
			r.atLineStart = true
			return
		}
		if r.mathBuf.Len() > 0 {
			r.mathBuf.WriteByte('\n')
		}
		if r.mathBuf.Len()+len(line) <= maxDisplayMathBytes {
			r.mathBuf.WriteString(line)
		}
		r.atLineStart = true
		return
	}
	if r.mathLine.Len() < maxDisplayMathBytes {
		r.mathLine.WriteRune(current)
	}
}

func (r *LiveMarkdownRenderer) writeFenceHeaderRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		language := "plaintext"
		if fields := strings.Fields(r.fenceHeader.String()); len(fields) > 0 {
			language = fields[0]
		}
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
		r.inCodeBlock = true
		r.codeLanguage = language
		r.atLineStart = true
		return
	}

	if current == '`' {
		// CommonMark: backtick fence info strings cannot contain backticks.
		raw := r.prefix.String() + r.fenceHeader.String() + string(current)
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
		r.fenceLen = 0
		r.atLineStart = false
		for _, ch := range raw {
			r.writeInlineRune(ch, plain)
		}
		return
	}

	r.fenceHeader.WriteRune(current)
	if r.fenceHeader.Len() <= maxLiveFenceHeaderBytes {
		return
	}

	r.inline.EndLine()
	plain.WriteString(r.prefix.String())
	plain.WriteString(r.fenceHeader.String())
	r.prefix.Reset()
	r.fenceHeader.Reset()
	r.inFenceHeader = false
	r.fenceLen = 0
	r.atLineStart = false
}

func (r *LiveMarkdownRenderer) writeCodeRune(current rune, plain *strings.Builder) {
	if r.codeOverflow {
		plain.WriteRune(current)
		if current == '\n' {
			r.codeOverflow = false
			r.atLineStart = true
		}
		return
	}

	if current == '\n' {
		line := r.codeLine.String()
		r.codeLine.Reset()
		if isClosingCodeFence(line, r.fenceLen) {
			r.inCodeBlock = false
			r.fenceLen = 0
			r.codeLanguage = ""
			r.atLineStart = true
			r.inline.Reset()
			return
		}
		r.flushPlain(plain)
		r.writeHighlightedCodeLine(line, true)
		r.atLineStart = true
		return
	}

	r.codeLine.WriteRune(current)
	if r.codeLine.Len() <= maxLiveCodeLineBytes {
		return
	}

	r.flushPlain(plain)
	if r.thinking {
		r.emitVisible(style.NewStyle().Foreground(thinkingAccentColor(r.theme)).Render("  " + r.codeLine.String()))
	} else {
		r.emitVisible(r.codeLine.String())
	}
	r.codeLine.Reset()
	r.codeOverflow = true
}

func (r *LiveMarkdownRenderer) writeInlineRune(current rune, plain *strings.Builder) {
	if current == '\n' {
		r.flushPlain(plain)
		r.inline.EndLine()
		r.emitVisible("\n")
		r.atLineStart = true
		return
	}
	r.flushPlain(plain)
	r.inline.WriteRune(current)
	r.endedWithNewline = false
}

func (r *LiveMarkdownRenderer) finishInlineLine(plain *strings.Builder) {
	r.flushPlain(plain)
	r.inline.EndLine()
}

func (r *LiveMarkdownRenderer) emitStyledMarker(marker string, markerColor color.Color, plain *strings.Builder) {
	r.flushPlain(plain)
	fmt.Fprint(r.w, style.NewStyle().Foreground(markerColor).Render(marker))
	r.endedWithNewline = false
}

func (r *LiveMarkdownRenderer) writeHighlightedCodeLine(line string, newline bool) {
	if r.thinking {
		fmt.Fprint(r.w, style.NewStyle().Foreground(thinkingAccentColor(r.theme)).Render("  "+line))
		if newline {
			fmt.Fprint(r.w, "\n")
		}
		r.endedWithNewline = newline
		return
	}
	language := r.codeLanguage
	if language == "" {
		language = "plaintext"
	}
	if err := syntax.HighlightWithoutTrailingNewline(r.w, line, language, r.theme.ChromaStyle); err != nil {
		fmt.Fprint(r.w, line)
	}
	if newline {
		fmt.Fprint(r.w, "\n")
	}
	r.endedWithNewline = newline
}

func (r *LiveMarkdownRenderer) flushPlain(plain *strings.Builder) {
	if plain.Len() == 0 {
		return
	}
	text := plain.String()
	plain.Reset()
	if r.thinking {
		RenderRawReasoningContent(r.w, text, r.theme)
		r.endedWithNewline = strings.HasSuffix(text, "\n")
		return
	}
	r.emitVisible(text)
}

func (r *LiveMarkdownRenderer) emitVisible(content string) {
	if content == "" {
		return
	}
	fmt.Fprint(r.w, content)
	r.endedWithNewline = strings.HasSuffix(content, "\n")
}

// Flush ensures all buffered content has been written out.
func (r *LiveMarkdownRenderer) Flush() {
	var plain strings.Builder
	for _, ch := range strings.ToValidUTF8(r.utf8Pending, "") {
		r.writeRune(ch, &plain)
	}
	r.utf8Pending = ""
	r.flushTable(&plain, true)

	if r.inMathHeader {
		line := r.mathHeader.String()
		r.mathHeader.Reset()
		r.inMathHeader = false
		r.finishMathHeaderLine(line, false, &plain)
	}

	if r.inMathBlock {
		line := r.mathLine.String()
		r.mathLine.Reset()
		if prefix, closed := isClosingMathBlock(line, r.mathKind, r.mathEnv); closed {
			if prefix != "" {
				if r.mathBuf.Len() > 0 {
					r.mathBuf.WriteByte('\n')
				}
				r.mathBuf.WriteString(prefix)
			}
		} else if line != "" {
			if r.mathBuf.Len() > 0 {
				r.mathBuf.WriteByte('\n')
			}
			r.mathBuf.WriteString(line)
		}
		if r.mathBuf.Len() > 0 {
			r.flushPlain(&plain)
			renderDisplayMathBlock(r.w, r.mathBuf.String(), r.theme, r.thinking)
			r.endedWithNewline = false
		}
		r.inMathBlock = false
		r.mathKind = mathBlockNone
		r.mathEnv = ""
		r.mathBuf.Reset()
	}

	if r.inFenceHeader {
		r.inline.EndLine()
		plain.WriteString(r.prefix.String())
		plain.WriteString(r.fenceHeader.String())
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
		r.fenceLen = 0
	}

	if r.prefix.Len() > 0 {
		prefix := r.prefix.String()
		r.prefix.Reset()
		r.atLineStart = false
		trimmed := strings.TrimSpace(prefix)
		if _, _, isFence := parseOpeningCodeFence(prefix); isFence {
			// A bare trailing code fence opens an empty code block, matching batch replay.
		} else if trimmed == "----" {
			r.flushPlain(&plain)
			r.inline.EndLine()
			r.emitVisible(" ")
		} else if trimmed == ">" {
			markerColor := r.theme.Border
			if r.thinking {
				markerColor = thinkingBaseColor(r.theme)
			}
			r.emitStyledMarker("┃ ", markerColor, &plain)
			r.inline.SetBlock(MarkdownQuote, 0)
		} else {
			for _, prefixRune := range prefix {
				r.writeInlineRune(prefixRune, &plain)
			}
		}
	}

	if r.inCodeBlock && !r.codeOverflow && r.codeLine.Len() > 0 {
		line := r.codeLine.String()
		if !isClosingCodeFence(line, r.fenceLen) {
			r.flushPlain(&plain)
			r.writeHighlightedCodeLine(line, false)
		}
	}

	r.codeLine.Reset()
	r.codeOverflow = false
	r.inCodeBlock = false
	r.fenceLen = 0
	r.codeLanguage = ""
	r.finishInlineLine(&plain)
	r.flushPlain(&plain)
	r.atLineStart = r.endedWithNewline
}

// EnsureTrailingNewline ensures the rendered output ends with a newline.
func (r *LiveMarkdownRenderer) EnsureTrailingNewline() {
	r.Flush()
	if r.endedWithNewline {
		return
	}
	r.emitVisible("\n")
	r.atLineStart = true
}

// ResetAtLineStart resets the renderer state to the start of a fresh line.
func (r *LiveMarkdownRenderer) ResetAtLineStart() {
	r.table = liveMarkdownTable{}
	r.utf8Pending = ""
	r.prefix.Reset()
	r.fenceHeader.Reset()
	r.codeLine.Reset()
	r.inFenceHeader = false
	r.fenceLen = 0
	r.inCodeBlock = false
	r.codeOverflow = false
	r.codeLanguage = ""
	r.inMathHeader = false
	r.mathHeader.Reset()
	r.inMathBlock = false
	r.mathKind = mathBlockNone
	r.mathEnv = ""
	r.mathLine.Reset()
	r.mathBuf.Reset()
	r.inline.Reset()
	r.atLineStart = true
	r.endedWithNewline = true
}

// EndedWithNewline returns whether the last written character was a newline.
func (r *LiveMarkdownRenderer) EndedWithNewline() bool {
	return r.endedWithNewline
}

func classifyLivePrefix(prefix string) livePrefixDecision {
	leadingSpaces := 0
	for leadingSpaces < len(prefix) && prefix[leadingSpaces] == ' ' {
		leadingSpaces++
	}
	if leadingSpaces > 3 {
		return livePrefixDecision{kind: livePrefixPlain}
	}

	body := prefix[leadingSpaces:]
	if body == "" {
		return livePrefixDecision{kind: livePrefixPending}
	}

	// Handle prefixes that can terminate on newline or trailing whitespace before the generic CR/LF reject.
	switch body[0] {
	case '`':
		ticks := 0
		for ticks < len(body) && body[ticks] == '`' {
			ticks++
		}
		if ticks == len(body) {
			if ticks < 64 {
				return livePrefixDecision{kind: livePrefixPending}
			}
			return livePrefixDecision{kind: livePrefixPlain}
		}
		if ticks >= 3 {
			return livePrefixDecision{
				kind:      livePrefixFence,
				fenceLen:  ticks,
				remainder: body[ticks:],
			}
		}
		return livePrefixDecision{kind: livePrefixPlain}

	case '>':
		if body == ">" {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if body == "> " {
			return livePrefixDecision{kind: livePrefixQuote}
		}
		// Align with replay markdown: both ">foo" (without space) and ">\n" are blockquotes.
		return livePrefixDecision{kind: livePrefixQuote, remainder: body[1:]}

	case '$':
		if body == "$" {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if strings.HasPrefix(body, "$$") {
			return livePrefixDecision{kind: livePrefixMathLine}
		}
		return livePrefixDecision{kind: livePrefixPlain}

	case '\\':
		if body == `\` {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if strings.HasPrefix(body, `\[`) {
			return livePrefixDecision{kind: livePrefixMathLine}
		}
		if strings.HasPrefix(`\begin{`, body) {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if strings.HasPrefix(body, `\begin{`) {
			return livePrefixDecision{kind: livePrefixMathLine}
		}
		return livePrefixDecision{kind: livePrefixPlain}

	case '-':
		if len(body) == 1 {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if len(body) == 2 && body[1] == ' ' {
			return livePrefixDecision{kind: livePrefixBullet}
		}
		// Match "----" horizontal rule consistently with replay markdown.
		if body == "--" || body == "---" || body == "----" {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if strings.HasPrefix(body, "----") && len(body) < 64 {
			tail := body[4:]
			if strings.HasSuffix(tail, "\n") {
				if strings.TrimSpace(tail) == "" {
					return livePrefixDecision{kind: livePrefixRule}
				}
				return livePrefixDecision{kind: livePrefixPlain}
			}
			if strings.Trim(tail, " \t\r") == "" {
				return livePrefixDecision{kind: livePrefixPending}
			}
		}
		return livePrefixDecision{kind: livePrefixPlain}
	}

	if strings.ContainsAny(body, "\r\n") {
		return livePrefixDecision{kind: livePrefixPlain}
	}
	// A literal bullet uses the same primary-colored marker as '-' and '*'.
	if body == "•" {
		return livePrefixDecision{kind: livePrefixPending}
	}
	if body == "• " {
		return livePrefixDecision{kind: livePrefixBullet}
	}

	switch body[0] {
	case '|':
		return livePrefixDecision{kind: livePrefixTable}
	case '#':
		hashes := 0
		for hashes < len(body) && body[hashes] == '#' {
			hashes++
		}
		if hashes == len(body) && hashes <= 6 {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if hashes >= 1 && hashes <= 6 && len(body) == hashes+1 && body[hashes] == ' ' {
			return livePrefixDecision{kind: livePrefixHeading, headingLevel: hashes}
		}
	case '+', '*':
		if len(body) == 1 {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if len(body) == 2 && body[1] == ' ' {
			return livePrefixDecision{kind: livePrefixBullet}
		}
	default:
		if body[0] >= '0' && body[0] <= '9' {
			digits := 0
			for digits < len(body) && body[digits] >= '0' && body[digits] <= '9' {
				digits++
			}
			if digits == len(body) && digits <= 9 {
				return livePrefixDecision{kind: livePrefixPending}
			}
			if digits <= 9 && digits < len(body) && body[digits] == '.' {
				if len(body) == digits+1 {
					return livePrefixDecision{kind: livePrefixPending}
				}
				if len(body) == digits+2 && body[digits+1] == ' ' {
					return livePrefixDecision{
						kind:   livePrefixNumbered,
						marker: body[:digits+1],
					}
				}
			}
		}
	}

	return livePrefixDecision{kind: livePrefixPlain}
}
