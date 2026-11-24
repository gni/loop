package markdown

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxMathTokens = 4096
	maxMathDepth  = 24
)

type texTokKind uint8

const (
	tokEOF texTokKind = iota
	tokText
	tokCmd
	tokLBrace
	tokRBrace
	tokLBrack
	tokRBrack
	tokCaret
	tokUnderscore
	tokAmp
	tokNewline // \\
	tokSpace
)

type texToken struct {
	kind texTokKind
	val  string
}

func tokenizeLatex(src string) []texToken {
	var tokens []texToken
	i := 0
	for i < len(src) && len(tokens) < maxMathTokens {
		ch := src[i]
		switch ch {
		case ' ', '\t', '\r', '\n':
			for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
				i++
			}
			tokens = append(tokens, texToken{kind: tokSpace, val: " "})
		case '{':
			tokens = append(tokens, texToken{kind: tokLBrace, val: "{"})
			i++
		case '}':
			tokens = append(tokens, texToken{kind: tokRBrace, val: "}"})
			i++
		case '[':
			tokens = append(tokens, texToken{kind: tokLBrack, val: "["})
			i++
		case ']':
			tokens = append(tokens, texToken{kind: tokRBrack, val: "]"})
			i++
		case '^':
			tokens = append(tokens, texToken{kind: tokCaret, val: "^"})
			i++
		case '_':
			tokens = append(tokens, texToken{kind: tokUnderscore, val: "_"})
			i++
		case '&':
			tokens = append(tokens, texToken{kind: tokAmp, val: "&"})
			i++
		case '~':
			tokens = append(tokens, texToken{kind: tokSpace, val: " "})
			i++
		case '\\':
			i++
			if i >= len(src) {
				tokens = append(tokens, texToken{kind: tokText, val: "\\"})
				break
			}
			next := src[i]
			if next == '\\' {
				i++
				// Skip optional spacing argument like \\[4pt]
				if i < len(src) && src[i] == '[' {
					end := strings.IndexByte(src[i:], ']')
					if end > 0 && end < 16 {
						i += end + 1
					}
				}
				tokens = append(tokens, texToken{kind: tokNewline, val: `\\`})
				continue
			}
			if isASCIIAlpha(next) {
				start := i
				for i < len(src) && isASCIIAlpha(src[i]) {
					i++
				}
				// Include trailing '*' for starred commands/operators if any.
				tokens = append(tokens, texToken{kind: tokCmd, val: src[start:i]})
			} else if next == '\n' || next == '\r' || next == '\t' {
				i++
				tokens = append(tokens, texToken{kind: tokSpace, val: " "})
			} else {
				r, sz := utf8.DecodeRuneInString(src[i:])
				i += sz
				tokens = append(tokens, texToken{kind: tokCmd, val: string(r)})
			}
		default:
			r, sz := utf8.DecodeRuneInString(src[i:])
			i += sz
			tokens = append(tokens, texToken{kind: tokText, val: string(r)})
		}
	}
	return tokens
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

type mathNodeKind uint8

const (
	nodeSeq    mathNodeKind = iota
	nodeAtom                // literal text, Greek symbol, operator, or space
	nodeScript              // base with optional sub and/or sup
	nodeFrac                // \frac{num}{den}
	nodeBinom               // \binom{n}{k}
	nodeSqrt                // \sqrt[index]{body}
	nodeAccent              // \hat{x}, \bar{x}, \vec{x}, ...
	nodeDelim               // \left( ... \right)
	nodeMatrix              // \begin{aligned|cases|pmatrix|...} ... \end{...}
	nodeText                // \text{...}, \mathrm{...}
	nodeSpace               // explicit math spacing (\,, \quad, ...)
)

type mathNode struct {
	kind     mathNodeKind
	text     string
	isOp     bool // true for big operators (\sum, \int, \lim, ...)
	isBinary bool // true for binary operators/relations (+, -, =, \to, ...)
	children []*mathNode
	base     *mathNode
	sub      *mathNode
	sup      *mathNode
	num      *mathNode
	den      *mathNode
	index    *mathNode
	left     string
	right    string
	env      string
	rows     [][]*mathNode // matrix / aligned rows of cell sequences
}

type texParser struct {
	tokens []texToken
	pos    int
	depth  int
}

func parseLatex(src string) *mathNode {
	p := &texParser{tokens: tokenizeLatex(src)}
	first := p.parseSeq(tokNewline, "", "")
	if p.peek().kind != tokNewline {
		return first
	}
	// Bare multi-line display equations separated by '\\' (without \begin{aligned})
	// stack vertically as separate rows.
	node := &mathNode{kind: nodeMatrix, env: "gather"}
	node.rows = append(node.rows, []*mathNode{first})
	for p.peek().kind == tokNewline {
		p.next()
		p.skipSpaces()
		if p.peek().kind == tokEOF {
			break
		}
		row := p.parseSeq(tokNewline, "", "")
		if len(row.children) > 0 {
			node.rows = append(node.rows, []*mathNode{row})
		}
	}
	return node
}

func (p *texParser) peek() texToken {
	if p.pos >= len(p.tokens) {
		return texToken{kind: tokEOF}
	}
	return p.tokens[p.pos]
}

func (p *texParser) next() texToken {
	if p.pos >= len(p.tokens) {
		return texToken{kind: tokEOF}
	}
	tok := p.tokens[p.pos]
	p.pos++
	return tok
}

func (p *texParser) skipSpaces() {
	for p.peek().kind == tokSpace {
		p.pos++
	}
}

func (p *texParser) parseSeq(stopTok texTokKind, stopCmd, stopEnv string) *mathNode {
	if p.depth >= maxMathDepth {
		return &mathNode{kind: nodeSeq}
	}
	p.depth++
	defer func() { p.depth-- }()

	seq := &mathNode{kind: nodeSeq}
	for {
		tok := p.peek()
		if tok.kind == tokEOF {
			break
		}
		if stopTok == tokNewline && tok.kind == tokNewline {
			break
		}
		if stopTok == tokAmp && (tok.kind == tokAmp || tok.kind == tokNewline) {
			break
		}
		if stopTok != tokEOF && tok.kind == stopTok {
			p.next()
			break
		}
		if stopCmd != "" && tok.kind == tokCmd && tok.val == stopCmd {
			break
		}
		if stopEnv != "" && tok.kind == tokCmd && tok.val == "end" {
			break
		}
		if tok.kind == tokSpace {
			p.next()
			continue
		}
		item := p.parseScriptedAtom()
		if item != nil {
			seq.children = append(seq.children, item)
		}
		// Handle classic TeX infix operators inside a group: {n \atop k}, {n \choose k}, {a \over b}.
		p.skipSpaces()
		if next := p.peek(); next.kind == tokCmd && (next.val == "atop" || next.val == "choose" || next.val == "over") {
			op := next.val
			p.next()
			left := &mathNode{kind: nodeSeq, children: seq.children}
			right := p.parseSeq(stopTok, stopCmd, stopEnv)
			var combined *mathNode
			switch op {
			case "over":
				combined = &mathNode{kind: nodeFrac, num: left, den: right}
			case "choose":
				combined = &mathNode{kind: nodeBinom, num: left, den: right}
			default: // "atop"
				combined = &mathNode{kind: nodeBinom, text: "atop", num: left, den: right}
			}
			return &mathNode{kind: nodeSeq, children: []*mathNode{combined}}
		}
	}
	return seq
}

func (p *texParser) parseScriptedAtom() *mathNode {
	base := p.parseAtom()
	if base == nil {
		return nil
	}

	var sub, sup *mathNode
	for {
		p.skipSpaces()
		tok := p.peek()
		if tok.kind == tokCmd && (tok.val == "limits" || tok.val == "nolimits") {
			p.next()
			continue
		}
		if tok.kind == tokUnderscore {
			p.next()
			p.skipSpaces()
			sub = p.parseArg()
			continue
		}
		if tok.kind == tokCaret {
			p.next()
			p.skipSpaces()
			sup = p.parseArg()
			continue
		}
		// Handle common LLM typo like \sum{i=1}^{n} (missing '_' before limit group).
		if base.isOp && sub == nil && tok.kind == tokLBrace && p.looksLikeLowerLimit() {
			sub = p.parseArg()
			continue
		}
		break
	}
	if sub != nil || sup != nil {
		return &mathNode{
			kind: nodeScript,
			base: base,
			sub:  sub,
			sup:  sup,
			isOp: base.isOp,
		}
	}
	return base
}

func (p *texParser) looksLikeLowerLimit() bool {
	if p.pos+2 >= len(p.tokens) || p.tokens[p.pos].kind != tokLBrace {
		return false
	}
	depth := 0
	hasEqOrTo := false
	for i := p.pos; i < len(p.tokens) && i < p.pos+24; i++ {
		switch p.tokens[i].kind {
		case tokLBrace:
			depth++
		case tokRBrace:
			depth--
			if depth == 0 {
				return hasEqOrTo
			}
		case tokText:
			if p.tokens[i].val == "=" {
				hasEqOrTo = true
			}
		case tokCmd:
			if p.tokens[i].val == "to" || p.tokens[i].val == "in" || p.tokens[i].val == "rightarrow" {
				hasEqOrTo = true
			}
		}
	}
	return false
}

func (p *texParser) parseArg() *mathNode {
	p.skipSpaces()
	if p.peek().kind == tokLBrace {
		p.next()
		return p.parseSeq(tokRBrace, "", "")
	}
	return p.parseAtom()
}

func (p *texParser) parseRawGroup() string {
	p.skipSpaces()
	if p.peek().kind != tokLBrace {
		if p.peek().kind == tokText {
			return p.next().val
		}
		return ""
	}
	p.next()
	var b strings.Builder
	depth := 1
	for p.pos < len(p.tokens) && depth > 0 {
		tok := p.next()
		switch tok.kind {
		case tokLBrace:
			depth++
			b.WriteByte('{')
		case tokRBrace:
			depth--
			if depth > 0 {
				b.WriteByte('}')
			}
		case tokCmd:
			if sym, ok := latexSymbols[tok.val]; ok {
				b.WriteString(sym)
			} else if tok.val == " " || tok.val == "," || tok.val == ";" || tok.val == ":" {
				b.WriteByte(' ')
			} else {
				b.WriteString(tok.val)
			}
		default:
			b.WriteString(tok.val)
		}
	}
	return b.String()
}

func (p *texParser) parseDelimChar() string {
	p.skipSpaces()
	tok := p.peek()
	switch tok.kind {
	case tokText:
		p.next()
		if tok.val == "." {
			return ""
		}
		return tok.val
	case tokLBrack:
		p.next()
		return "["
	case tokRBrack:
		p.next()
		return "]"
	case tokCmd:
		p.next()
		switch tok.val {
		case "{", "lbrace":
			return "{"
		case "}", "rbrace":
			return "}"
		case "|", "Vert", "lVert", "rVert":
			return "‖"
		case "vert", "lvert", "rvert":
			return "|"
		case "langle":
			return "⟨"
		case "rangle":
			return "⟩"
		case "lfloor":
			return "⌊"
		case "rfloor":
			return "⌋"
		case "lceil":
			return "⌈"
		case "rceil":
			return "⌉"
		case ".":
			return ""
		default:
			if sym, ok := latexSymbols[tok.val]; ok {
				return sym
			}
			return tok.val
		}
	default:
		return ""
	}
}

func (p *texParser) parseAtom() *mathNode {
	if p.depth >= maxMathDepth {
		if p.pos < len(p.tokens) {
			p.pos++
		}
		return nil
	}
	tok := p.next()
	switch tok.kind {
	case tokEOF, tokRBrace:
		return nil
	case tokLBrace:
		return p.parseSeq(tokRBrace, "", "")
	case tokLBrack:
		return &mathNode{kind: nodeAtom, text: "["}
	case tokRBrack:
		return &mathNode{kind: nodeAtom, text: "]"}
	case tokAmp:
		return &mathNode{kind: nodeSpace, text: " "}
	case tokNewline:
		return &mathNode{kind: nodeSpace, text: " "}
	case tokText:
		txt := tok.val
		if txt == "'" {
			txt = "′"
		}
		return &mathNode{kind: nodeAtom, text: txt, isBinary: latexBinaryOps[txt]}
	case tokCmd:
		return p.parseCommand(tok.val)
	default:
		return nil
	}
}

func (p *texParser) parseCommand(cmd string) *mathNode {
	switch cmd {
	// Fractions and binomials
	case "frac", "dfrac", "tfrac", "cfrac":
		num := p.parseArg()
		den := p.parseArg()
		return &mathNode{kind: nodeFrac, num: num, den: den}
	case "binom", "dbinom", "tbinom":
		num := p.parseArg()
		den := p.parseArg()
		return &mathNode{kind: nodeBinom, num: num, den: den}

	// Roots
	case "sqrt":
		var idx *mathNode
		p.skipSpaces()
		if p.peek().kind == tokLBrack {
			p.next()
			idx = p.parseSeq(tokRBrack, "", "")
		}
		body := p.parseArg()
		return &mathNode{kind: nodeSqrt, index: idx, base: body}

	// Delimiters
	case "left":
		left := p.parseDelimChar()
		body := p.parseSeq(tokEOF, "right", "")
		right := ""
		if p.peek().kind == tokCmd && p.peek().val == "right" {
			p.next()
			right = p.parseDelimChar()
		}
		return &mathNode{kind: nodeDelim, left: left, right: right, base: body}
	case "right":
		// Stray \right without matching \left
		d := p.parseDelimChar()
		if d == "" {
			return nil
		}
		return &mathNode{kind: nodeAtom, text: d}
	case "bigl", "Bigl", "biggl", "Biggl":
		closeCmd := cmd[:len(cmd)-1] + "r"
		if p.hasMatchingCommand(closeCmd) {
			left := p.parseDelimChar()
			body := p.parseSeq(tokEOF, closeCmd, "")
			right := ""
			if p.peek().kind == tokCmd && p.peek().val == closeCmd {
				p.next()
				right = p.parseDelimChar()
			}
			return &mathNode{kind: nodeDelim, left: left, right: right, base: body}
		}
		d := p.parseDelimChar()
		if d == "" {
			return nil
		}
		return &mathNode{kind: nodeAtom, text: d}
	case "big", "Big", "bigg", "Bigg", "bigr", "Bigr", "biggr", "Biggr", "middle":
		d := p.parseDelimChar()
		if d == "" {
			return nil
		}
		return &mathNode{kind: nodeAtom, text: d}

	// Negated relations (\not=, \not\in, \not\equiv, ...)
	case "not":
		return p.parseNotRelation()

	// Stacked relations & extensible arrows
	case "overset", "stackrel":
		top := p.parseArg()
		base := p.parseArg()
		return &mathNode{kind: nodeScript, base: base, sup: top, isOp: true}
	case "underset":
		bot := p.parseArg()
		base := p.parseArg()
		return &mathNode{kind: nodeScript, base: base, sub: bot, isOp: true}
	case "xrightarrow", "xleftarrow":
		var sub *mathNode
		p.skipSpaces()
		if p.peek().kind == tokLBrack {
			p.next()
			sub = p.parseSeq(tokRBrack, "", "")
		}
		sup := p.parseArg()
		arrow := "→"
		if cmd == "xleftarrow" {
			arrow = "←"
		}
		base := &mathNode{kind: nodeAtom, text: arrow, isBinary: true}
		if sub != nil || sup != nil {
			return &mathNode{kind: nodeScript, base: base, sub: sub, sup: sup, isOp: true}
		}
		return base
	case "overbrace", "underbrace":
		body := p.parseArg()
		return &mathNode{kind: nodeAccent, text: cmd, base: body, isOp: true}
	case "pmod":
		arg := p.parseArg()
		children := []*mathNode{
			{kind: nodeAtom, text: "mod"},
			{kind: nodeSpace, text: " "},
		}
		if arg != nil {
			children = append(children, arg)
		}
		return &mathNode{kind: nodeDelim, left: "(", right: ")", base: &mathNode{kind: nodeSeq, children: children}, isOp: true}
	case "pod":
		arg := p.parseArg()
		return &mathNode{kind: nodeDelim, left: "(", right: ")", base: arg}

	// Environments
	case "begin":
		env := strings.TrimSpace(p.parseRawGroup())
		// Consume mandatory spec argument for \begin{array}{lcc} and \begin{alignedat}{2}.
		switch env {
		case "array", "alignedat", "alignedat*", "alignat", "alignat*":
			p.skipSpaces()
			if p.peek().kind == tokLBrace {
				_ = p.parseRawGroup()
			}
		}
		return p.parseEnvironment(env)
	case "ce":
		raw := p.parseRawGroup()
		return &mathNode{kind: nodeText, text: formatChemFormula(raw)}
	case "end":
		_ = p.parseRawGroup()
		return nil

	// Accents
	case "hat", "widehat", "tilde", "widetilde", "bar", "overline", "vec", "dot", "ddot", "acute", "grave", "check", "breve", "underline":
		body := p.parseArg()
		return &mathNode{kind: nodeAccent, text: cmd, base: body}

	// Math alphabets
	case "mathbb", "mathcal", "mathscr", "mathbf", "mathfrak":
		raw := p.parseRawGroup()
		return &mathNode{kind: nodeAtom, text: mapMathAlphabet(cmd, raw)}
	case "boldsymbol", "bm":
		if p.groupHasComplexCmd() {
			return p.parseArg()
		}
		raw := p.parseRawGroup()
		return &mathNode{kind: nodeAtom, text: mapMathAlphabet(cmd, raw)}

	// Operator name (supports optional '*' for stacked limits like \operatorname*{argmin})
	case "operatorname":
		isLim := false
		p.skipSpaces()
		if p.peek().kind == tokText && p.peek().val == "*" {
			p.next()
			isLim = true
		}
		raw := p.parseRawGroup()
		return &mathNode{kind: nodeAtom, text: raw, isOp: isLim}

	// Text and upright spans
	case "text", "textrm", "textup", "textit", "textbf", "mathrm", "mathit", "mathnormal", "mathsf", "mathtt", "mbox":
		raw := p.parseRawGroup()
		return &mathNode{kind: nodeText, text: raw}
	case "cancel", "bcancel", "xcancel", "smash", "mathclap", "mathllap", "mathrlap":
		return p.parseArg()

	// Display-style hints (formatting-only, consume transparently)
	case "displaystyle", "textstyle", "scriptstyle", "scriptscriptstyle", "limits", "nolimits", "notag", "nonumber", "strut", "rm", "bf", "it", "cal":
		return nil
	case "tag", "label", "ref", "eqref", "hspace", "vspace", "phantom", "hphantom", "vphantom":
		_ = p.parseRawGroup()
		return nil
	case "color", "textcolor":
		_ = p.parseRawGroup()
		if cmd == "textcolor" {
			return p.parseArg()
		}
		return nil
	case "boxed":
		body := p.parseArg()
		return &mathNode{kind: nodeDelim, left: "[", right: "]", base: body}

	// Math spacing commands
	case ",", ":", ";", " ", "thinspace", "medspace", "thickspace", "enspace":
		return &mathNode{kind: nodeSpace, text: " "}
	case "quad":
		return &mathNode{kind: nodeSpace, text: "  "}
	case "qquad":
		return &mathNode{kind: nodeSpace, text: "    "}
	case "!", "negthinspace":
		return nil

	// Literal escaped symbols
	case "{", "}", "$", "%", "#", "_", "&":
		return &mathNode{kind: nodeAtom, text: cmd}
	case "|":
		return &mathNode{kind: nodeAtom, text: "‖"}
	}

	// Known upright operators (\lim, \sin, \log, \max, ...)
	if op, ok := latexOperators[cmd]; ok {
		return &mathNode{kind: nodeAtom, text: op, isOp: latexLimitOps[cmd]}
	}
	// Known Unicode math symbols (\alpha, \sum, \int, \infty, \to, ...)
	if sym, ok := latexSymbols[cmd]; ok {
		return &mathNode{
			kind:     nodeAtom,
			text:     sym,
			isOp:     latexLimitOps[cmd],
			isBinary: latexBinaryOps[sym],
		}
	}
	// Unknown command fallback: preserve readable command name cleanly.
	if len(cmd) == 1 && !unicode.IsLetter(rune(cmd[0])) {
		return &mathNode{kind: nodeAtom, text: cmd}
	}
	return &mathNode{kind: nodeAtom, text: "\\" + cmd}
}

func (p *texParser) parseEnvironment(env string) *mathNode {
	if p.depth >= maxMathDepth {
		return &mathNode{kind: nodeMatrix, env: env}
	}
	p.depth++
	defer func() { p.depth-- }()

	node := &mathNode{kind: nodeMatrix, env: env}
	var currentRow []*mathNode
	for {
		cell := p.parseSeq(tokAmp, "", env)
		currentRow = append(currentRow, cell)

		tok := p.peek()
		if tok.kind == tokAmp {
			p.next()
			continue
		}
		if tok.kind == tokNewline {
			p.next()
			node.rows = append(node.rows, currentRow)
			currentRow = nil
			p.skipSpaces()
			if p.peek().kind == tokCmd && p.peek().val == "end" {
				p.next()
				_ = p.parseRawGroup()
				break
			}
			continue
		}
		if tok.kind == tokCmd && tok.val == "end" {
			p.next()
			_ = p.parseRawGroup()
			if !isEmptyRow(currentRow) {
				node.rows = append(node.rows, currentRow)
			}
			break
		}
		// EOF before \end{env}: flush gracefully.
		if !isEmptyRow(currentRow) {
			node.rows = append(node.rows, currentRow)
		}
		break
	}
	return node
}

func isEmptyRow(cells []*mathNode) bool {
	for _, c := range cells {
		if c != nil && len(c.children) > 0 {
			return false
		}
	}
	return true
}

func (p *texParser) parseNotRelation() *mathNode {
	p.skipSpaces()
	tok := p.peek()
	negated := ""
	switch tok.kind {
	case tokText:
		switch tok.val {
		case "=":
			negated = "≠"
		case "<":
			negated = "≮"
		case ">":
			negated = "≯"
		}
	case tokCmd:
		switch tok.val {
		case "in":
			negated = "∉"
		case "subset":
			negated = "⊄"
		case "subseteq":
			negated = "⊈"
		case "supset":
			negated = "⊅"
		case "supseteq":
			negated = "⊉"
		case "equiv":
			negated = "≢"
		case "approx":
			negated = "≉"
		case "sim":
			negated = "≁"
		case "cong":
			negated = "≇"
		case "mid":
			negated = "∤"
		case "le", "leq", "leqslant":
			negated = "≰"
		case "ge", "geq", "geqslant":
			negated = "≱"
		}
	}
	if negated != "" {
		p.next()
		return &mathNode{kind: nodeAtom, text: negated, isBinary: true}
	}
	return &mathNode{kind: nodeAtom, text: "¬"}
}

func (p *texParser) hasMatchingCommand(target string) bool {
	depth := 0
	for i := p.pos; i < len(p.tokens); i++ {
		switch p.tokens[i].kind {
		case tokLBrace:
			depth++
		case tokRBrace:
			if depth == 0 {
				return false
			}
			depth--
		case tokCmd:
			if depth == 0 && p.tokens[i].val == target {
				return true
			}
		}
	}
	return false
}

func (p *texParser) groupHasComplexCmd() bool {
	i := p.pos
	for i < len(p.tokens) && p.tokens[i].kind == tokSpace {
		i++
	}
	if i >= len(p.tokens) || p.tokens[i].kind != tokLBrace {
		return false
	}
	depth := 0
	for ; i < len(p.tokens); i++ {
		switch p.tokens[i].kind {
		case tokLBrace:
			depth++
		case tokRBrace:
			depth--
			if depth == 0 {
				return false
			}
		case tokCmd:
			if _, ok := latexSymbols[p.tokens[i].val]; !ok {
				return true
			}
		}
	}
	return false
}

// formatChemFormula renders basic mhchem \ce{...} expressions cleanly in Unicode
// (e.g. "H2SO4 -> H+ + HSO4-" -> "H₂SO₄ → H⁺ + HSO₄⁻").
func formatChemFormula(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "<=>", "⇌")
	s = strings.ReplaceAll(s, "->", "→")
	s = strings.ReplaceAll(s, "<-", "←")

	runes := []rune(s)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		// Reaction arrow labels like "→[+3e-]": render cleanly with superscript charge.
		if r == '[' {
			end := i + 1
			for end < len(runes) && runes[end] != ']' {
				end++
			}
			if end < len(runes) {
				b.WriteRune('[')
				b.WriteString(formatChemFormula(string(runes[i+1 : end])))
				b.WriteRune(']')
				i = end
				continue
			}
		}
		// Explicit superscript charge like "SO4^2-" -> "SO₄²⁻"
		if r == '^' && i+1 < len(runes) {
			j := i + 1
			for j < len(runes) && (runes[j] >= '0' && runes[j] <= '9' || runes[j] == '+' || runes[j] == '-') {
				if mapped, ok := superscriptMap[runes[j]]; ok {
					b.WriteRune(mapped)
				} else {
					b.WriteRune(runes[j])
				}
				j++
			}
			i = j - 1
			continue
		}
		// Stoichiometric subscripts: digits immediately after a letter or ')'
		// (e.g. "H2SO4" -> "H₂SO₄", "HSO4-" -> "HSO₄⁻").
		if r >= '0' && r <= '9' && i > 0 && (unicode.IsLetter(runes[i-1]) || runes[i-1] == ')') {
			for i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
				b.WriteRune(subscriptMap[runes[i]])
				i++
			}
			i--
			continue
		}
		// Trailing ion charge '+' or '-' immediately attached to an element/digit/bracket.
		if (r == '+' || r == '-') && i > 0 && runes[i-1] != ' ' && runes[i-1] != '[' &&
			(i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == ',' || runes[i+1] == ']') {
			b.WriteRune(superscriptMap[r])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
