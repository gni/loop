package markdown

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

type fixedWidthWriter struct {
	bytes.Buffer
	width int
}

func (w *fixedWidthWriter) MarkdownWidth() int { return w.width }

func TestLatexInlineExpressions(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`E=mc^2`, `E = mc²`},
		{`\alpha + \beta`, `α + β`},
		{`\mathcal{L}`, `ℒ`},
		{`\mathbb{R}`, `ℝ`},
		{`|y-\hat{y}|`, `|y - ŷ|`},
		{`(y-\hat{y})^2`, `(y - ŷ)²`},
		{`10^6`, `10⁶`},
		{`e^{-x^2}`, `e⁻ˣ²`},
		{`\frac{1}{2}`, `½`},
		{`\frac{a}{b}`, `a/b`},
		{`\left( \frac{a}{b} \right)`, `(a/b)`},
		{`\sqrt{x}`, `√x`},
		{`\sqrt{x^2 + 1}`, `√(x² + 1)`},
		{`x_{i+1}`, `xᵢ₊₁`},
		{`\forall x \in \mathbb{R}, x^2 \ge 0`, `∀x ∈ ℝ, x² ≥ 0`},
		{`\mathcal{O}(n \log n)`, `𝒪(n log n)`},
		{`2\sin x + \cos^2 \theta = 1`, `2 sin x + cos² θ = 1`},
		{`x \not= y \land x \not\in S`, `x ≠ y ∧ x ∉ S`},
		{`a \equiv b \pmod{n}`, `a ≡ b (mod n)`},
		{`\bm{\hat{\theta}}`, `θ̂`},
		{`\left\{ x \in \mathbb{R} \middle| x > 0 \right\}`, `{x ∈ ℝ|x > 0}`},
		{`\operatorname*{argmin}_{x} f(x)`, `argminₓ f(x)`},
		{`g: Y \twoheadrightarrow Z`, `g: Y ↠ Z`},
		{`\ce{H2SO4 -> H+ + HSO4-}`, `H₂SO₄ → H⁺ + HSO₄⁻`},
		{`\oiint_{\partial V} \vec{F}\cdot d\vec{S} = 0`, `∯_(∂V) F⃗ · dS⃗ = 0`},
		{`\Cov(X, Y)`, `Cov(X, Y)`},
		{`\sigma_X \sigma_Y`, `σ_X σ_Y`},
		{`\nabla_{\theta}\mathcal{L}`, `∇_θ ℒ`},
	}
	for _, tc := range cases {
		got := renderLatexInline(tc.src)
		if got != tc.want {
			t.Errorf("renderLatexInline(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestInlineMarkdownMathAndCurrencyGuards(t *testing.T) {
	theme := style.GetTheme("studio")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "inline dollar and paren math",
			input: `Inline math: $E=mc^2$ and \( \alpha + \beta \)`,
			want:  `Inline math: E = mc² and α + β`,
		},
		{
			name:  "currency pair not treated as math",
			input: `It costs $5 and $10 today.`,
			want:  `It costs $5 and $10 today.`,
		},
		{
			name:  "currency followed by valid inline math",
			input: `Price is $5, where $x_1^2 + y_1^2 = r^2$.`,
			want:  `Price is $5, where x₁² + y₁² = r².`,
		},
		{
			name:  "escaped dollar literal",
			input: `Use \$ for a literal dollar.`,
			want:  `Use $ for a literal dollar.`,
		},
		{
			name:  "code span protects raw latex",
			input: "Code `$E=mc^2$` stays raw.",
			want:  "Code $E=mc^2$ stays raw.",
		},
		{
			name:  "underscore inside math protected from italics",
			input: `Subscript $x_1 + x_2$ stays intact.`,
			want:  `Subscript x₁ + x₂ stays intact.`,
		},
		{
			name:  "inline bracket math mid-line",
			input: `Inline display: \[ \frac{a+b}{c+d} \] end.`,
			want:  `Inline display: (a + b)/(c + d) end.`,
		},
		{
			name:  "dollar slash double-dollar syntax stays literal",
			input: `GitHub: $/$$ math supported.`,
			want:  `GitHub: $/$$ math supported.`,
		},
		{
			name:  "unclosed double-dollar preserves empty brackets and later math",
			input: `Display math: doubled $$ or \[ \] and inline $E=mc^2$.`,
			want:  `Display math: doubled $$ or \[ \] and inline E = mc².`,
		},
		{
			name:  "single dollar label before inline math",
			input: `Inline math - single $: $E=mc^2$ and \( \alpha + \beta \)`,
			want:  `Inline math - single $: E = mc² and α + β`,
		},
		{
			name:  "html nbsp entities decode cleanly outside code",
			input: "Euler: $e^{i\\pi}+1=0$ &nbsp; Code `&nbsp;` &amp; stays",
			want:  "Euler: e^(iπ) + 1 = 0   Code &nbsp; &amp; stays",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := style.StripAnsi(RenderInlineMarkdown(tc.input, false, theme))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDisplayMath2DLayout(t *testing.T) {
	// 1. Summation + stacked fraction
	sumLines := renderLatexDisplay(`\sum_{i=1}^{n} i = \frac{n(n+1)}{2}`, 80)
	sumJoined := strings.Join(sumLines, "\n")
	if len(sumLines) < 3 {
		t.Fatalf("expected multi-line 2D sum+frac layout, got %q", sumJoined)
	}
	if !strings.Contains(sumJoined, "∑") || !strings.Contains(sumJoined, "─") || !strings.Contains(sumJoined, "i=1") {
		t.Fatalf("unexpected sum+frac output:\n%s", sumJoined)
	}

	// 2. Integral with stacked limits
	intLines := renderLatexDisplay(`\int_0^\infty e^{-x}\,dx = 1`, 80)
	intJoined := strings.Join(intLines, "\n")
	if !strings.Contains(intJoined, "∫") || !strings.Contains(intJoined, "∞") || !strings.Contains(intJoined, "e⁻ˣ") {
		t.Fatalf("unexpected integral output:\n%s", intJoined)
	}

	// 3. Aligned environment aligns '=' vertically across rows
	alignSrc := `\begin{aligned}
  f'(x) &= \lim_{h\to 0} \frac{f(x+h)-f(x)}{h} \\
  g(x)  &= e^{-x^2}
\end{aligned}`
	alignLines := renderLatexDisplay(alignSrc, 80)
	alignJoined := strings.Join(alignLines, "\n")
	if !strings.Contains(alignJoined, "f′(x)") || !strings.Contains(alignJoined, "lim") || !strings.Contains(alignJoined, "h→0") || !strings.Contains(alignJoined, "e⁻ˣ²") {
		t.Fatalf("unexpected aligned output:\n%s", alignJoined)
	}
	eqCols := []int{}
	for _, line := range alignLines {
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			eqCols = append(eqCols, style.CellWidth(line[:idx]))
		}
	}
	if len(eqCols) != 2 || eqCols[0] != eqCols[1] {
		t.Fatalf("expected '=' aligned across rows, got columns %v in:\n%s", eqCols, alignJoined)
	}

	// 4. Tall extensible delimiters & matrix
	matLines := renderLatexDisplay(`\begin{pmatrix} a & b \\ c & d \end{pmatrix}`, 80)
	matJoined := strings.Join(matLines, "\n")
	if !strings.Contains(matJoined, "⎛") || !strings.Contains(matJoined, "⎠") {
		t.Fatalf("expected tall extensible parens for pmatrix:\n%s", matJoined)
	}

	// 5. Bare multi-line display math separated by '\\'
	bareLines := renderLatexDisplay(`a = b + c \\ d = e + f`, 80)
	if len(bareLines) != 2 {
		t.Fatalf("expected 2 stacked lines for bare multi-line display math, got %d:\n%s", len(bareLines), strings.Join(bareLines, "\n"))
	}

	// 6. Multi-line parenthesized fraction with superscript
	powLines := renderLatexDisplay(`\left(\frac{a+b}{c}\right)^2`, 80)
	powJoined := strings.Join(powLines, "\n")
	if !strings.Contains(powJoined, "⎞²") {
		t.Fatalf("expected superscript ² on tall closing paren:\n%s", powJoined)
	}

	// 7. Tall nth-root radical and overline
	rootLines := renderLatexDisplay(`\sqrt[3]{\frac{1+x^2}{2}}`, 80)
	rootJoined := strings.Join(rootLines, "\n")
	if !strings.Contains(rootJoined, "³") || !strings.Contains(rootJoined, "╲╱") {
		t.Fatalf("expected 3rd-root index on tall radical:\n%s", rootJoined)
	}
	overLines := renderLatexDisplay(`\overline{x + y}`, 80)
	if len(overLines) != 2 || !strings.Contains(overLines[0], "─") {
		t.Fatalf("expected 2D overline bar above x + y:\n%s", strings.Join(overLines, "\n"))
	}

	// 8. Narrow-width aligned fallback preserves separate rows
	narrowAlign := renderLatexDisplay(alignSrc, 18)
	if len(narrowAlign) != 2 {
		t.Fatalf("expected narrow aligned fallback to keep 2 rows, got %d:\n%s", len(narrowAlign), strings.Join(narrowAlign, "\n"))
	}

	// 9. Pipe inside $$...$$ and \[...\] table cells stays inside the cell
	if cells, ok := markdownTableCells(`| L1 | $$|y-\hat{y}|$$ | \[|a|\] |`); !ok || len(cells) != 3 {
		t.Fatalf("expected 3 table cells with protected math pipes, got ok=%v cells=%q", ok, cells)
	}

	// 10. alignedat{2} consumes column count argument without leaking '2'
	alignedatLines := renderLatexDisplay(`\begin{alignedat}{2} x &= 1 &\qquad y &= 2 \\ x^2 &= 1 &\qquad y^2 &= 4 \end{alignedat}`, 80)
	alignedatJoined := strings.Join(alignedatLines, "\n")
	if len(alignedatLines) != 2 || strings.Contains(alignedatJoined, "2x") {
		t.Fatalf("unexpected alignedat output:\n%s", alignedatJoined)
	}

	// 11. underbrace draws horizontal brace & xrightarrow stretches arrow shaft
	ubLines := renderLatexDisplay(`\underbrace{a + b + c}_{\text{terms}}`, 80)
	ubJoined := strings.Join(ubLines, "\n")
	if len(ubLines) != 3 || !strings.Contains(ubJoined, "╰───┬───╯") {
		t.Fatalf("expected 3-line underbrace with horizontal brace:\n%s", ubJoined)
	}
	xaLines := renderLatexDisplay(`X \xrightarrow{\circ g} Y`, 80)
	xaJoined := strings.Join(xaLines, "\n")
	if !strings.Contains(xaJoined, "─→") {
		t.Fatalf("expected stretched extensible arrow shaft:\n%s", xaJoined)
	}

	// 12. Stirling \left\{ { n \atop k } \right\}, \prod_{p\;\text{prime}}, and paired \Bigl(\frac{...}{...}\Bigr)
	stirlingLines := renderLatexDisplay(`\left\{ { n \atop k } \right\}`, 80)
	stirlingJoined := strings.Join(stirlingLines, "\n")
	if len(stirlingLines) < 2 || strings.Contains(stirlingJoined, `\atop`) || !strings.Contains(stirlingJoined, "⎧") {
		t.Fatalf("expected 2D Stirling curly braces around n atop k:\n%s", stirlingJoined)
	}
	prodLines := renderLatexDisplay(`\prod_{p\;\text{prime}} \frac{1}{1-p^{-s}}`, 80)
	prodJoined := strings.Join(prodLines, "\n")
	if !strings.Contains(prodJoined, "p prime") {
		t.Fatalf("expected preserved space in limit subscript 'p prime':\n%s", prodJoined)
	}
	biglLines := renderLatexDisplay(`\sin\Bigl(\frac{\pi s}{2}\Bigr)`, 80)
	biglJoined := strings.Join(biglLines, "\n")
	if !strings.Contains(biglJoined, "⎛") || !strings.Contains(biglJoined, "⎠") {
		t.Fatalf("expected tall extensible parens for paired \\Bigl(...\\Bigr):\n%s", biglJoined)
	}
}

func TestLiveAndBatchMarkdownLatexParity(t *testing.T) {
	theme := style.GetTheme("studio")
	doc := strings.Join([]string{
		`Inline: $E=mc^2$ and \( \alpha + \beta \)`,
		``,
		`$$`,
		`\sum_{i=1}^{n} i = \frac{n(n+1)}{2}`,
		`$$`,
		``,
		`\[`,
		`\int_0^\infty e^{-x}\,dx = 1`,
		`\]`,
		``,
		`\begin{aligned}`,
		`  f'(x) &= \lim_{h\to 0} \frac{f(x+h)-f(x)}{h} \\`,
		`  g(x)  &= e^{-x^2}`,
		`\end{aligned}`,
		``,
		`| Model | Loss \(\mathcal{L}\) | Params \(N\) |`,
		`| --- | :---: | ---: |`,
		`| L1 | \(|y-\hat{y}|\) | \(10^6\) |`,
		`| L2 | \((y-\hat{y})^2\) | \(10^7\) |`,
		``,
		`Currency check: $5 and $10, escaped \$ stays literal.`,
	}, "\n")

	var batchOut fixedWidthWriter
	batchOut.width = 90
	RenderMarkdownContent(&batchOut, doc, theme)

	var liveOut fixedWidthWriter
	liveOut.width = 90
	live := NewLiveMarkdownRenderer(&liveOut, theme)
	// Stream 1 byte at a time to stress chunk boundaries across $, \(, \[, \begin{.
	for i := 0; i < len(doc); i++ {
		live.Write(doc[i : i+1])
	}
	live.Flush()

	batchPlain := style.StripAnsi(batchOut.String())
	livePlain := style.StripAnsi(liveOut.String())

	// Non-table sections must match character-for-character between batch and 1-byte streaming
	// (tables intentionally reserve wider streaming heuristic padding in table_layout.go).
	batchHead, _, _ := strings.Cut(batchPlain, "╭")
	liveHead, _, _ := strings.Cut(livePlain, "╭")
	if batchHead != liveHead {
		t.Fatalf("batch vs live mismatch before table:\n--- BATCH ---\n%s\n--- LIVE ---\n%s", batchHead, liveHead)
	}
	_, batchTail, _ := strings.Cut(batchPlain, "╯")
	_, liveTail, _ := strings.Cut(livePlain, "╯")
	if batchTail != liveTail {
		t.Fatalf("batch vs live mismatch after table:\n--- BATCH ---\n%s\n--- LIVE ---\n%s", batchTail, liveTail)
	}

	// Verify rendered symbols and table math cells in both batch and live outputs
	for _, mode := range []struct {
		name string
		out  string
	}{{"batch", batchPlain}, {"live", livePlain}} {
		for _, want := range []string{
			"E = mc²",
			"α + β",
			"∑",
			"n(n + 1)",
			"∫",
			"e⁻ˣ",
			"f′(x)",
			"h→0",
			"e⁻ˣ²",
			"Loss ℒ",
			"|y - ŷ|",
			"(y - ŷ)²",
			"10⁶",
			"10⁷",
			"$5 and $10",
			"escaped $ stays literal",
		} {
			if !strings.Contains(mode.out, want) {
				t.Errorf("[%s] expected rendered output to contain %q:\n%s", mode.name, want, mode.out)
			}
		}
		for _, leaked := range []string{`\frac`, `\alpha`, `\mathcal`, `\hat`, `\begin{aligned}`, `\int`} {
			if strings.Contains(mode.out, leaked) {
				t.Errorf("[%s] raw LaTeX %q leaked into output:\n%s", mode.name, leaked, mode.out)
			}
		}
	}
}

func TestLatexHostileAndMalformedInputs(t *testing.T) {
	theme := style.GetTheme("studio")
	inputs := []string{
		strings.Repeat("{", 200) + "x" + strings.Repeat("}", 200),
		`$\frac{\frac{\frac{a}{b}}{c}}$`,
		`$$\begin{aligned} a &= b`,
		`\( unclosed math with *stars* and _under_`,
		`$$ \sqrt{\frac{1}{2}} $$`,
		`$`,
		`$$`,
		`\begin{unknownenv} x \end{unknownenv}`,
	}
	for _, in := range inputs {
		var out fixedWidthWriter
		out.width = 24
		RenderMarkdownContent(&out, in, theme)
		_ = renderLatexDisplay(in, 16)
	}
}
