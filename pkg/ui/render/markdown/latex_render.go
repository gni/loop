package markdown

import (
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/style"
)

// renderLatexInline converts a LaTeX math expression into clean single-line
// Unicode typography suitable for inline prose and Markdown table cells.
func renderLatexInline(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	ast := parseLatex(trimmed)
	return strings.TrimSpace(formatInlineNode(ast, false))
}

// renderLatexDisplay converts a display LaTeX expression ($$...$$, \[...\],
// \begin{aligned}...\end{aligned}, matrices, etc.) into aligned 2D Unicode rows.
// Falls back cleanly to compact inline text when an expression exceeds maxWidth.
func renderLatexDisplay(src string, maxWidth int) []string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return nil
	}
	if maxWidth < 16 {
		maxWidth = 16
	}
	ast := parseLatex(trimmed)
	box := buildMathBox(ast, maxWidth)
	if box.width > maxWidth {
		// Graceful fallback: for multi-row environments (aligned, gather, cases),
		// preserve line breaks by falling back row-by-row instead of collapsing all
		// rows onto one wide line.
		matrixNode := ast
		if matrixNode.kind == nodeSeq && len(matrixNode.children) == 1 {
			matrixNode = matrixNode.children[0]
		}
		if matrixNode.kind == nodeMatrix && len(matrixNode.rows) > 1 {
			var lines []string
			sep := ", "
			if matrixNode.env == "aligned" || matrixNode.env == "align" || matrixNode.env == "align*" ||
				matrixNode.env == "alignedat" || matrixNode.env == "alignedat*" ||
				matrixNode.env == "alignat" || matrixNode.env == "alignat*" ||
				matrixNode.env == "flalign" || matrixNode.env == "flalign*" ||
				matrixNode.env == "split" || matrixNode.env == "gather" || matrixNode.env == "gather*" {
				sep = " "
			}
			for _, row := range matrixNode.rows {
				var cols []string
				for _, cell := range row {
					if s := strings.TrimSpace(formatInlineNode(cell, false)); s != "" {
						cols = append(cols, s)
					}
				}
				if len(cols) > 0 {
					lines = append(lines, strings.Join(cols, sep))
				}
			}
			if len(lines) > 0 {
				return lines
			}
		}
		inline := renderLatexInline(trimmed)
		if inline == "" {
			return nil
		}
		return []string{inline}
	}
	// Trim empty leading/trailing rows and trailing spaces per line.
	lines := make([]string, 0, len(box.lines))
	for _, l := range box.lines {
		lines = append(lines, strings.TrimRight(l, " "))
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// --- Inline Unicode formatter ---

func formatInlineNode(n *mathNode, compact bool) string {
	if n == nil {
		return ""
	}
	switch n.kind {
	case nodeSeq:
		return formatInlineSeq(n.children, compact)
	case nodeAtom:
		return n.text
	case nodeText:
		return n.text
	case nodeSpace:
		if compact {
			if n.text != "" {
				return " "
			}
			return ""
		}
		return n.text
	case nodeAccent:
		return applyLatexAccent(n.text, formatInlineNode(n.base, true))
	case nodeScript:
		base := formatInlineNode(n.base, compact)
		var b strings.Builder
		b.WriteString(base)
		if n.sub != nil {
			subStr := formatInlineNode(n.sub, true)
			if subStr != "" {
				b.WriteString(formatSubscriptInline(subStr))
			}
		}
		if n.sup != nil {
			supStr := formatInlineNode(n.sup, true)
			if supStr != "" {
				b.WriteString(formatSuperscriptInline(supStr))
			}
		}
		return b.String()
	case nodeFrac:
		num := formatInlineNode(n.num, compact)
		den := formatInlineNode(n.den, compact)
		if vf, ok := vulgarFractions[num+"/"+den]; ok {
			return vf
		}
		if needsInlineParens(n.num) {
			num = "(" + num + ")"
		}
		if needsInlineParens(n.den) {
			den = "(" + den + ")"
		}
		return num + "/" + den
	case nodeBinom:
		num := formatInlineNode(n.num, compact)
		den := formatInlineNode(n.den, compact)
		if n.text == "atop" {
			return num + ", " + den
		}
		return "C(" + num + ", " + den + ")"
	case nodeSqrt:
		body := formatInlineNode(n.base, compact)
		prefix := "√"
		if n.index != nil {
			idx := formatInlineNode(n.index, true)
			if mapped, ok := toSuperscript(idx); ok {
				prefix = mapped + "√"
			} else if idx != "" {
				prefix = idx + "√"
			}
		}
		if utf8.RuneCountInString(body) > 1 && !isWrappedParens(body) {
			return prefix + "(" + body + ")"
		}
		return prefix + body
	case nodeDelim:
		body := formatInlineNode(n.base, compact)
		return n.left + body + n.right
	case nodeMatrix:
		var rowStrs []string
		for _, row := range n.rows {
			var cols []string
			for _, cell := range row {
				cols = append(cols, formatInlineNode(cell, compact))
			}
			sep := ", "
			if n.env == "aligned" || n.env == "align" || n.env == "align*" || n.env == "split" {
				sep = " "
			}
			rowStrs = append(rowStrs, strings.TrimSpace(strings.Join(cols, sep)))
		}
		joined := strings.Join(rowStrs, "; ")
		l, r := envDelimiters(n.env)
		return l + joined + r
	}
	return ""
}

func formatInlineSeq(children []*mathNode, compact bool) string {
	var b strings.Builder
	var prev *mathNode
	for idx, ch := range children {
		if ch == nil {
			continue
		}
		if ch.kind == nodeSpace {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
				if compact {
					if ch.text != "" {
						b.WriteByte(' ')
					}
				} else {
					b.WriteString(ch.text)
				}
			}
			prev = ch
			continue
		}
		rendered := formatInlineNode(ch, compact)
		if rendered == "" {
			continue
		}
		if !compact {
			isBin := ch.kind == nodeAtom && ch.isBinary
			isUnarySign := isBin && (ch.text == "-" || ch.text == "+" || ch.text == "±") && isUnaryContext(prev)

			if isBin && !isUnarySign {
				if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
					b.WriteByte(' ')
				}
				b.WriteString(rendered)
				if idx+1 < len(children) {
					b.WriteByte(' ')
				}
				prev = ch
				continue
			}

			// Comma / semicolon / colon trailing breath space (keep ':=' tight).
			if ch.kind == nodeAtom && (ch.text == "," || ch.text == ";" || ch.text == ":") {
				b.WriteString(rendered)
				nextIsEq := idx+1 < len(children) && children[idx+1] != nil && children[idx+1].kind == nodeAtom && children[idx+1].text == "="
				if idx+1 < len(children) && !(ch.text == ":" && nextIsEq) {
					b.WriteByte(' ')
				}
				prev = ch
				continue
			}

			// Space before named upright operators (e.g. "n log n", "2 sin x")
			// and after operators (e.g. "sin x", "cos² θ", "∑ i").
			// Non-limit function operators keep function calls tight: "Cov(X, Y)", "sin(x)".
			if prev != nil && !strings.HasSuffix(b.String(), " ") {
				spaceAfterPrev := needsSpaceAfter(prev) && !isClosingPunct(rendered) && !(isCallOpener(ch) && !isLimitScriptOrOp(prev))
				spaceBeforeCur := needsSpaceBefore(ch) && !isOpeningNode(prev)
				spaceAfterFallbackScript := endsWithASCIIFallbackScript(prev, b.String()) && startsWithIdentifier(rendered)
				if spaceAfterPrev || spaceBeforeCur || spaceAfterFallbackScript {
					b.WriteByte(' ')
				}
			}
		}
		b.WriteString(rendered)
		prev = ch
	}
	return b.String()
}

func isUnaryContext(prev *mathNode) bool {
	if prev == nil {
		return true
	}
	if prev.kind == nodeAtom {
		if prev.isBinary || prev.isOp {
			return true
		}
		switch prev.text {
		case "(", "[", "{", "|", "‖", "⟨", ",", ";", ":":
			return true
		}
	}
	return false
}

func needsSpaceAfter(prev *mathNode) bool {
	if prev == nil {
		return false
	}
	if prev.kind == nodeAtom && (prev.isOp || (len(prev.text) > 1 && isAllASCIIAlpha(prev.text))) {
		return true
	}
	if prev.kind == nodeScript {
		return prev.isOp || needsSpaceAfter(prev.base)
	}
	return false
}

func needsSpaceBefore(cur *mathNode) bool {
	if cur == nil {
		return false
	}
	if cur.isOp {
		return true
	}
	if cur.kind == nodeAtom && len(cur.text) > 1 && isAllASCIIAlpha(cur.text) {
		return true
	}
	if cur.kind == nodeScript {
		return needsSpaceBefore(cur.base)
	}
	return false
}

func isOpeningNode(n *mathNode) bool {
	if n == nil || n.kind == nodeSpace {
		return true
	}
	if n.kind == nodeAtom {
		if n.isBinary {
			return true
		}
		switch n.text {
		case "(", "[", "{", "|", "‖", "⟨", ",", ";", ":":
			return true
		}
	}
	return false
}

func isAllASCIIAlpha(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return len(s) > 0
}

func isClosingPunct(s string) bool {
	switch s {
	case ")", "]", "}", "⟩", ",", ";", ".", ":", "′":
		return true
	}
	return false
}

func isCallOpener(n *mathNode) bool {
	if n == nil {
		return false
	}
	if n.kind == nodeAtom && (n.text == "(" || n.text == "[") {
		return true
	}
	if n.kind == nodeDelim && (n.left == "(" || n.left == "[") {
		return true
	}
	return false
}

func isLimitScriptOrOp(n *mathNode) bool {
	if n == nil {
		return false
	}
	return n.isOp
}

func endsWithASCIIFallbackScript(prev *mathNode, renderedSoFar string) bool {
	if prev == nil || prev.kind != nodeScript || prev.isOp {
		return false
	}
	if len(renderedSoFar) < 2 {
		return false
	}
	last, sz := utf8.DecodeLastRuneInString(renderedSoFar)
	if last == ')' {
		return false
	}
	beforeLast, _ := utf8.DecodeLastRuneInString(renderedSoFar[:len(renderedSoFar)-sz])
	return beforeLast == '_' || beforeLast == '^'
}

func startsWithIdentifier(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= 0x0370 && r <= 0x03FF) ||
		(r >= 0x2100 && r <= 0x214F) ||
		(r >= 0x1D400 && r <= 0x1D7FF)
}

func needsInlineParens(n *mathNode) bool {
	if n == nil || n.kind != nodeSeq {
		return false
	}
	for i, c := range n.children {
		if c != nil && c.kind == nodeAtom && c.isBinary && i > 0 {
			return true
		}
	}
	return false
}

func isWrappedParens(s string) bool {
	return len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')'
}

var isAlreadySuperscript = func() map[rune]bool {
	m := make(map[rune]bool, len(superscriptMap))
	for _, v := range superscriptMap {
		m[v] = true
	}
	return m
}()

var isAlreadySubscript = func() map[rune]bool {
	m := make(map[rune]bool, len(subscriptMap))
	for _, v := range subscriptMap {
		m[v] = true
	}
	return m
}()

func toSuperscript(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if isAlreadySuperscript[r] {
			b.WriteRune(r)
			continue
		}
		mapped, ok := superscriptMap[r]
		if !ok {
			return "", false
		}
		b.WriteRune(mapped)
	}
	return b.String(), b.Len() > 0
}

func toSubscript(s string) (string, bool) {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if isAlreadySubscript[r] {
			b.WriteRune(r)
			continue
		}
		mapped, ok := subscriptMap[r]
		if !ok {
			return "", false
		}
		b.WriteRune(mapped)
	}
	return b.String(), b.Len() > 0
}

func formatSuperscriptInline(s string) string {
	if mapped, ok := toSuperscript(s); ok {
		return mapped
	}
	if utf8.RuneCountInString(s) == 1 {
		return "^" + s
	}
	return "^(" + s + ")"
}

func formatSubscriptInline(s string) string {
	if mapped, ok := toSubscript(s); ok {
		return mapped
	}
	if utf8.RuneCountInString(s) == 1 {
		return "_" + s
	}
	return "_(" + s + ")"
}

func envDelimiters(env string) (string, string) {
	switch env {
	case "pmatrix":
		return "(", ")"
	case "bmatrix":
		return "[", "]"
	case "Bmatrix":
		return "{", "}"
	case "vmatrix":
		return "|", "|"
	case "Vmatrix":
		return "‖", "‖"
	case "cases":
		return "{", ""
	default:
		return "", ""
	}
}

// --- 2D Monospace Box Layout Composer for Display Math ---

type mathBox struct {
	lines    []string
	width    int
	baseline int
}

func textBox(s string) mathBox {
	return mathBox{
		lines:    []string{s},
		width:    style.CellWidth(s),
		baseline: 0,
	}
}

func padCenter(s string, targetWidth int) string {
	w := style.CellWidth(s)
	if w >= targetWidth {
		return s
	}
	left := (targetWidth - w) / 2
	right := targetWidth - w - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

func padRight(s string, targetWidth int) string {
	w := style.CellWidth(s)
	if w >= targetWidth {
		return s
	}
	return s + strings.Repeat(" ", targetWidth-w)
}

func padLeft(s string, targetWidth int) string {
	w := style.CellWidth(s)
	if w >= targetWidth {
		return s
	}
	return strings.Repeat(" ", targetWidth-w) + s
}

func hstack(boxes ...mathBox) mathBox {
	filtered := boxes[:0]
	for _, b := range boxes {
		if len(b.lines) > 0 && b.width > 0 {
			filtered = append(filtered, b)
		}
	}
	if len(filtered) == 0 {
		return textBox("")
	}
	if len(filtered) == 1 {
		return filtered[0]
	}

	maxAbove := 0
	maxBelow := 0
	totalWidth := 0
	for _, b := range filtered {
		above := b.baseline
		below := len(b.lines) - 1 - b.baseline
		if above > maxAbove {
			maxAbove = above
		}
		if below > maxBelow {
			maxBelow = below
		}
		totalWidth += b.width
	}

	height := maxAbove + 1 + maxBelow
	lines := make([]string, height)
	for row := range height {
		var sb strings.Builder
		for _, b := range filtered {
			srcRow := row - (maxAbove - b.baseline)
			if srcRow >= 0 && srcRow < len(b.lines) {
				sb.WriteString(padRight(b.lines[srcRow], b.width))
			} else {
				sb.WriteString(strings.Repeat(" ", b.width))
			}
		}
		lines[row] = sb.String()
	}
	return mathBox{lines: lines, width: totalWidth, baseline: maxAbove}
}

func wrapExtensibleDelim(inner mathBox, left, right string) mathBox {
	h := len(inner.lines)
	if h <= 1 {
		var parts []mathBox
		if left != "" {
			parts = append(parts, textBox(left))
		}
		parts = append(parts, inner)
		if right != "" {
			parts = append(parts, textBox(right))
		}
		return hstack(parts...)
	}

	var parts []mathBox
	if left != "" {
		parts = append(parts, tallDelimBox(left, h, inner.baseline))
		parts = append(parts, textBox(" "))
	}
	parts = append(parts, inner)
	if right != "" {
		parts = append(parts, textBox(" "))
		parts = append(parts, tallDelimBox(right, h, inner.baseline))
	}
	return hstack(parts...)
}

func tallDelimBox(ch string, height, baseline int) mathBox {
	lines := make([]string, height)
	top, mid, bot, center := ch, ch, ch, ch
	switch ch {
	case "(":
		top, mid, bot, center = "⎛", "⎜", "⎝", "⎜"
	case ")":
		top, mid, bot, center = "⎞", "⎟", "⎠", "⎟"
	case "[":
		top, mid, bot, center = "⎡", "⎢", "⎣", "⎢"
	case "]":
		top, mid, bot, center = "⎤", "⎥", "⎦", "⎥"
	case "{":
		top, mid, bot, center = "⎧", "│", "⎩", "⎨"
	case "}":
		top, mid, bot, center = "⎫", "│", "⎭", "⎬"
	case "|":
		top, mid, bot, center = "│", "│", "│", "│"
	case "‖":
		top, mid, bot, center = "║", "║", "║", "║"
	case "⟨":
		top, mid, bot, center = "╱", "│", "╲", "⟨"
	case "⟩":
		top, mid, bot, center = "╲", "│", "╱", "⟩"
	case "⌊":
		top, mid, bot, center = "⎢", "⎢", "⎣", "⎢"
	case "⌋":
		top, mid, bot, center = "⎥", "⎥", "⎦", "⎥"
	case "⌈":
		top, mid, bot, center = "⎡", "⎢", "⎢", "⎢"
	case "⌉":
		top, mid, bot, center = "⎤", "⎥", "⎥", "⎥"
	}

	for r := range height {
		switch {
		case r == 0:
			lines[r] = top
		case r == height-1:
			lines[r] = bot
		case r == baseline:
			lines[r] = center
		default:
			lines[r] = mid
		}
	}
	return mathBox{lines: lines, width: style.CellWidth(top), baseline: baseline}
}

func buildMathBox(n *mathNode, maxWidth int) mathBox {
	if n == nil {
		return textBox("")
	}
	switch n.kind {
	case nodeSeq:
		return buildSeqBox(n.children, maxWidth)
	case nodeAtom:
		return textBox(n.text)
	case nodeText:
		return textBox(n.text)
	case nodeSpace:
		return textBox(n.text)
	case nodeAccent:
		inlineBody := formatInlineNode(n.base, true)
		if utf8.RuneCountInString(inlineBody) <= 1 {
			return textBox(applyLatexAccent(n.text, inlineBody))
		}
		if n.text == "overline" || n.text == "bar" {
			inner := buildMathBox(n.base, maxWidth)
			bar := strings.Repeat("─", inner.width)
			lines := append([]string{bar}, inner.lines...)
			return mathBox{lines: lines, width: inner.width, baseline: inner.baseline + 1}
		}
		return textBox(applyLatexAccent(n.text, inlineBody))
	case nodeScript:
		return buildScriptBox(n, maxWidth)
	case nodeFrac:
		return buildFracBox(n.num, n.den, maxWidth)
	case nodeBinom:
		num := buildMathBox(n.num, maxWidth-4)
		den := buildMathBox(n.den, maxWidth-4)
		w := max(num.width, den.width)
		lines := make([]string, 0, len(num.lines)+len(den.lines))
		for _, l := range num.lines {
			lines = append(lines, padCenter(l, w))
		}
		for _, l := range den.lines {
			lines = append(lines, padCenter(l, w))
		}
		inner := mathBox{lines: lines, width: w, baseline: len(num.lines) - 1}
		if n.text == "atop" {
			return inner
		}
		return wrapExtensibleDelim(inner, "(", ")")
	case nodeSqrt:
		return buildSqrtBox(n, maxWidth)
	case nodeDelim:
		inner := buildMathBox(n.base, maxWidth-4)
		return wrapExtensibleDelim(inner, n.left, n.right)
	case nodeMatrix:
		return buildMatrixBox(n, maxWidth)
	}
	return textBox("")
}

func buildSeqBox(children []*mathNode, maxWidth int) mathBox {
	var parts []mathBox
	var prev *mathNode
	for idx, ch := range children {
		if ch == nil {
			continue
		}
		if ch.kind == nodeSpace {
			if len(parts) > 0 {
				parts = append(parts, textBox(ch.text))
			}
			prev = ch
			continue
		}
		isBin := ch.kind == nodeAtom && ch.isBinary
		isUnarySign := isBin && (ch.text == "-" || ch.text == "+" || ch.text == "±") && isUnaryContext(prev)

		if isBin && !isUnarySign {
			if len(parts) > 0 && !endsWithSpaceBox(parts) {
				parts = append(parts, textBox(" "))
			}
			parts = append(parts, textBox(ch.text))
			if idx+1 < len(children) {
				parts = append(parts, textBox(" "))
			}
			prev = ch
			continue
		}
		if ch.kind == nodeAtom && (ch.text == "," || ch.text == ";" || ch.text == ":") {
			parts = append(parts, textBox(ch.text))
			nextIsEq := idx+1 < len(children) && children[idx+1] != nil && children[idx+1].kind == nodeAtom && children[idx+1].text == "="
			if idx+1 < len(children) && !(ch.text == ":" && nextIsEq) {
				parts = append(parts, textBox(" "))
			}
			prev = ch
			continue
		}
		box := buildMathBox(ch, maxWidth)
		if box.width == 0 {
			continue
		}
		if prev != nil && !endsWithSpaceBox(parts) {
			spaceAfterPrev := needsSpaceAfter(prev) && !isClosingBox(box) && !(isCallOpener(ch) && !isLimitScriptOrOp(prev))
			spaceBeforeCur := needsSpaceBefore(ch) && !isOpeningNode(prev)
			spaceAfterFallbackScript := len(parts) > 0 && len(parts[len(parts)-1].lines) == 1 &&
				endsWithASCIIFallbackScript(prev, parts[len(parts)-1].lines[0]) &&
				len(box.lines) == 1 && startsWithIdentifier(box.lines[0])
			if spaceAfterPrev || spaceBeforeCur || spaceAfterFallbackScript {
				parts = append(parts, textBox(" "))
			}
		}
		parts = append(parts, box)
		prev = ch
	}
	return hstack(parts...)
}

func endsWithSpaceBox(parts []mathBox) bool {
	if len(parts) == 0 {
		return false
	}
	last := parts[len(parts)-1]
	return len(last.lines) == 1 && strings.TrimSpace(last.lines[0]) == ""
}

func isClosingBox(b mathBox) bool {
	return len(b.lines) == 1 && isClosingPunct(b.lines[0])
}

func horizontalBrace(width int, under bool) string {
	if width <= 1 {
		if under {
			return "┬"
		}
		return "┴"
	}
	if width == 2 {
		if under {
			return "╰╯"
		}
		return "╭╮"
	}
	mid := width / 2
	left := strings.Repeat("─", mid-1)
	right := strings.Repeat("─", width-2-mid)
	if under {
		return "╰" + left + "┬" + right + "╯"
	}
	return "╭" + left + "┴" + right + "╮"
}

func buildScriptBox(n *mathNode, maxWidth int) mathBox {
	// Stack upper/lower limits vertically on big operators (\sum, \prod, \int, \lim, \overset, \underset, \underbrace, \overbrace).
	if n.isOp {
		var braceKind string
		baseNode := n.base
		if baseNode != nil && baseNode.kind == nodeAccent && (baseNode.text == "underbrace" || baseNode.text == "overbrace") {
			braceKind = baseNode.text
			baseNode = baseNode.base
		}
		opBox := buildMathBox(baseNode, maxWidth)
		var supStr, subStr string
		if n.sup != nil {
			supStr = formatInlineNode(n.sup, true)
		}
		if n.sub != nil {
			subStr = formatInlineNode(n.sub, true)
		}
		w := max(opBox.width, style.CellWidth(supStr), style.CellWidth(subStr))
		// Stretch extensible arrows (\xrightarrow, \xleftarrow) across the label width.
		if len(opBox.lines) == 1 && w > 1 && baseNode != nil && baseNode.kind == nodeAtom {
			if baseNode.text == "→" {
				opBox = textBox(strings.Repeat("─", w-1) + "→")
			} else if baseNode.text == "←" {
				opBox = textBox("←" + strings.Repeat("─", w-1))
			}
		}
		var lines []string
		baseline := opBox.baseline
		if supStr != "" {
			lines = append(lines, padCenter(supStr, w))
			baseline++
		}
		if braceKind == "overbrace" {
			lines = append(lines, padCenter(horizontalBrace(opBox.width, false), w))
			baseline++
		}
		for _, l := range opBox.lines {
			lines = append(lines, padCenter(l, w))
		}
		if braceKind == "underbrace" {
			lines = append(lines, padCenter(horizontalBrace(opBox.width, true), w))
		}
		if subStr != "" {
			lines = append(lines, padCenter(subStr, w))
		}
		return mathBox{lines: lines, width: w, baseline: baseline}
	}

	baseBox := buildMathBox(n.base, maxWidth)
	// Single-line base: prefer compact Unicode superscripts/subscripts so
	// expressions like e⁻ˣ² and xᵢ stay sleek and readable.
	if len(baseBox.lines) == 1 {
		var suffix strings.Builder
		if n.sub != nil {
			if s := formatInlineNode(n.sub, true); s != "" {
				suffix.WriteString(formatSubscriptInline(s))
			}
		}
		if n.sup != nil {
			if s := formatInlineNode(n.sup, true); s != "" {
				suffix.WriteString(formatSuperscriptInline(s))
			}
		}
		if suffix.Len() == 0 {
			return baseBox
		}
		return hstack(baseBox, textBox(suffix.String()))
	}

	// Multi-line base (e.g. tall parenthesized fraction raised to a power):
	// place Unicode superscript on the top row and subscript on the bottom row.
	h := len(baseBox.lines)
	supStr, subStr := "", ""
	if n.sup != nil {
		if raw := formatInlineNode(n.sup, true); raw != "" {
			if mapped, ok := toSuperscript(raw); ok {
				supStr = mapped
			} else {
				supStr = raw
			}
		}
	}
	if n.sub != nil {
		if raw := formatInlineNode(n.sub, true); raw != "" {
			if mapped, ok := toSubscript(raw); ok {
				subStr = mapped
			} else {
				subStr = raw
			}
		}
	}
	w := max(style.CellWidth(supStr), style.CellWidth(subStr))
	if w == 0 {
		return baseBox
	}
	scriptLines := make([]string, h)
	for r := range h {
		switch {
		case r == 0 && supStr != "":
			scriptLines[r] = padRight(supStr, w)
		case r == h-1 && subStr != "":
			scriptLines[r] = padRight(subStr, w)
		default:
			scriptLines[r] = strings.Repeat(" ", w)
		}
	}
	return hstack(baseBox, mathBox{lines: scriptLines, width: w, baseline: baseBox.baseline})
}

func buildFracBox(numNode, denNode *mathNode, maxWidth int) mathBox {
	num := buildMathBox(numNode, maxWidth)
	den := buildMathBox(denNode, maxWidth)
	w := max(num.width, den.width)
	if w == 0 {
		return textBox("")
	}
	bar := strings.Repeat("─", w)
	lines := make([]string, 0, len(num.lines)+1+len(den.lines))
	for _, l := range num.lines {
		lines = append(lines, padCenter(l, w))
	}
	lines = append(lines, bar)
	for _, l := range den.lines {
		lines = append(lines, padCenter(l, w))
	}
	return mathBox{lines: lines, width: w, baseline: len(num.lines)}
}

func buildSqrtBox(n *mathNode, maxWidth int) mathBox {
	body := buildMathBox(n.base, maxWidth-4)
	if len(body.lines) <= 1 {
		return textBox(formatInlineNode(n, false))
	}
	h := len(body.lines)
	idxLabel := ""
	if n.index != nil {
		rawIdx := formatInlineNode(n.index, true)
		if mapped, ok := toSuperscript(rawIdx); ok {
			idxLabel = mapped
		} else {
			idxLabel = rawIdx
		}
	}
	idxPad := 0
	if w := style.CellWidth(idxLabel); w > 1 {
		idxPad = w - 1
	}

	lines := make([]string, h+1)
	lines[0] = strings.Repeat(" ", idxPad+h+1) + "┌" + strings.Repeat("─", body.width)
	for r := range h {
		var prefix string
		if r == h-1 {
			prefix = strings.Repeat(" ", idxPad) + "╲╱" + strings.Repeat(" ", h)
		} else {
			leftSpaces := idxPad + h - r
			if r == h-2 && idxLabel != "" {
				w := style.CellWidth(idxLabel)
				prefix = idxLabel + strings.Repeat(" ", max(1, leftSpaces-w)) + "╱" + strings.Repeat(" ", r+1)
			} else {
				prefix = strings.Repeat(" ", leftSpaces) + "╱" + strings.Repeat(" ", r+1)
			}
		}
		lines[r+1] = prefix + padRight(body.lines[r], body.width)
	}
	width := style.CellWidth(lines[0])
	for _, l := range lines {
		if w := style.CellWidth(l); w > width {
			width = w
		}
	}
	return mathBox{lines: lines, width: width, baseline: body.baseline + 1}
}

func buildMatrixBox(n *mathNode, maxWidth int) mathBox {
	if len(n.rows) == 0 {
		return textBox("")
	}

	isAligned := n.env == "aligned" || n.env == "align" || n.env == "align*" ||
		n.env == "alignedat" || n.env == "alignedat*" ||
		n.env == "alignat" || n.env == "alignat*" ||
		n.env == "flalign" || n.env == "flalign*" ||
		n.env == "split"
	isCases := n.env == "cases"

	numCols := 0
	for _, r := range n.rows {
		if len(r) > numCols {
			numCols = len(r)
		}
	}
	colWidths := make([]int, numCols)
	grid := make([][]mathBox, len(n.rows))
	for ri, row := range n.rows {
		grid[ri] = make([]mathBox, numCols)
		for ci := range numCols {
			if ci < len(row) {
				b := buildMathBox(row[ci], maxWidth)
				grid[ri][ci] = b
				if b.width > colWidths[ci] {
					colWidths[ci] = b.width
				}
			} else {
				grid[ri][ci] = textBox("")
			}
		}
	}

	colGap := "  "
	if isAligned {
		colGap = " "
	} else if isCases {
		colGap = "   "
	}

	var outLines []string
	totalWidth := 0
	for ri := range grid {
		alignedRow := make([]mathBox, 0, numCols*2)
		for ci := range numCols {
			if ci > 0 {
				alignedRow = append(alignedRow, textBox(colGap))
			}
			cell := grid[ri][ci]
			w := colWidths[ci]
			padded := make([]string, len(cell.lines))
			for li, line := range cell.lines {
				switch {
				case isAligned && ci%2 == 0:
					padded[li] = padLeft(line, w)
				case isAligned || isCases:
					padded[li] = padRight(line, w)
				default:
					padded[li] = padCenter(line, w)
				}
			}
			alignedRow = append(alignedRow, mathBox{lines: padded, width: w, baseline: cell.baseline})
		}
		rowBox := hstack(alignedRow...)
		if rowBox.width > totalWidth {
			totalWidth = rowBox.width
		}
		outLines = append(outLines, rowBox.lines...)
	}

	for i, l := range outLines {
		outLines[i] = padRight(l, totalWidth)
	}
	inner := mathBox{
		lines:    outLines,
		width:    totalWidth,
		baseline: len(outLines) / 2,
	}
	left, right := envDelimiters(n.env)
	if left != "" || right != "" {
		return wrapExtensibleDelim(inner, left, right)
	}
	return inner
}
