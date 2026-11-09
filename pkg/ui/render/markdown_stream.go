package render

import (
	"fmt"
	"image/color"
	"io"
	"strings"
	"unicode/utf8"

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
)

type livePrefixDecision struct {
	kind         livePrefixKind
	headingLevel int
	marker       string
}

// LiveMarkdownRenderer incrementally styles streamed markdown text.
type LiveMarkdownRenderer struct {
	w     io.Writer
	theme style.UITheme

	atLineStart bool
	prefix      strings.Builder

	inFenceHeader bool
	fenceHeader   strings.Builder
	inCodeBlock   bool
	codeLanguage  string
	codeLine      strings.Builder
	codeOverflow  bool

	inline           *InlineMarkdownWriter
	table            liveMarkdownTable
	tablePassthrough bool
	utf8Pending      string

	endedWithNewline bool
}

// NewLiveMarkdownRenderer creates a new streaming markdown renderer.
func NewLiveMarkdownRenderer(w io.Writer, theme style.UITheme) *LiveMarkdownRenderer {
	return &LiveMarkdownRenderer{
		w:                w,
		theme:            theme,
		atLineStart:      true,
		endedWithNewline: true,
		inline:           NewInlineMarkdownWriter(w, theme, MarkdownBody, 0, false),
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
			r.inFenceHeader = true
			r.atLineStart = false
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
			r.emitStyledMarker("┃ ", r.theme.Border, plain)
			r.inline.SetBlock(MarkdownQuote, 0)
			return
		case livePrefixBullet:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			fmt.Fprintf(r.w, "  %s ", style.NewStyle().Foreground(r.theme.Primary).Render("•"))
			r.endedWithNewline = false
			return
		case livePrefixNumbered:
			r.prefix.Reset()
			r.atLineStart = false
			r.flushPlain(plain)
			number := style.NewStyle().Foreground(r.theme.Primary).Render(decision.marker)
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
		if isLiveCodeFence(line) {
			r.inCodeBlock = false
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
	r.emitVisible(r.codeLine.String())
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
	language := r.codeLanguage
	if language == "" {
		language = "plaintext"
	}
	if err := HighlightWithoutTrailingNewline(r.w, line, language, r.theme.ChromaStyle); err != nil {
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
	r.emitVisible(plain.String())
	plain.Reset()
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
	for _, ch := range strings.ToValidUTF8(r.utf8Pending, "�") {
		r.writeRune(ch, &plain)
	}
	r.utf8Pending = ""
	r.flushTable(&plain)

	if r.inFenceHeader {
		r.inline.EndLine()
		plain.WriteString(r.prefix.String())
		plain.WriteString(r.fenceHeader.String())
		r.prefix.Reset()
		r.fenceHeader.Reset()
		r.inFenceHeader = false
	}

	if r.prefix.Len() > 0 {
		prefix := r.prefix.String()
		r.prefix.Reset()
		r.atLineStart = false
		for _, prefixRune := range prefix {
			r.writeInlineRune(prefixRune, &plain)
		}
	}

	if r.inCodeBlock && !r.codeOverflow && r.codeLine.Len() > 0 {
		line := r.codeLine.String()
		if !isLiveCodeFence(line) {
			r.flushPlain(&plain)
			r.writeHighlightedCodeLine(line, false)
		}
	}

	r.codeLine.Reset()
	r.codeOverflow = false
	r.inCodeBlock = false
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
	r.inCodeBlock = false
	r.codeOverflow = false
	r.codeLanguage = ""
	r.inline.Reset()
	r.atLineStart = true
	r.endedWithNewline = true
}

// EndedWithNewline returns whether the last written character was a newline.
func (r *LiveMarkdownRenderer) EndedWithNewline() bool {
	return r.endedWithNewline
}

func isLiveCodeFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
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
	case '`':
		ticks := 0
		for ticks < len(body) && body[ticks] == '`' {
			ticks++
		}
		if ticks == len(body) {
			if ticks < 3 {
				return livePrefixDecision{kind: livePrefixPending}
			}
			if ticks == 3 {
				return livePrefixDecision{kind: livePrefixFence}
			}
		}
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
	case '>':
		if body == ">" {
			return livePrefixDecision{kind: livePrefixPending}
		}
		if body == "> " {
			return livePrefixDecision{kind: livePrefixQuote}
		}
	case '-', '+', '*':
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
