package markdown

// LaTeX command -> Unicode symbol lookup tables (zero external dependencies).
var latexSymbols = map[string]string{
	// Greek lowercase
	"alpha":      "α",
	"beta":       "β",
	"gamma":      "γ",
	"delta":      "δ",
	"epsilon":    "ε",
	"varepsilon": "ε",
	"zeta":       "ζ",
	"eta":        "η",
	"theta":      "θ",
	"vartheta":   "ϑ",
	"iota":       "ι",
	"kappa":      "κ",
	"lambda":     "λ",
	"mu":         "μ",
	"nu":         "ν",
	"xi":         "ξ",
	"omicron":    "ο",
	"pi":         "π",
	"varpi":      "ϖ",
	"rho":        "ρ",
	"varrho":     "ϱ",
	"sigma":      "σ",
	"varsigma":   "ς",
	"tau":        "τ",
	"upsilon":    "υ",
	"phi":        "φ",
	"varphi":     "φ",
	"chi":        "χ",
	"psi":        "ψ",
	"omega":      "ω",

	// Greek uppercase
	"Gamma":      "Γ",
	"Delta":      "Δ",
	"Theta":      "Θ",
	"Lambda":     "Λ",
	"Xi":         "Ξ",
	"Pi":         "Π",
	"Sigma":      "Σ",
	"Upsilon":    "Υ",
	"Phi":        "Φ",
	"Psi":        "Ψ",
	"Omega":      "Ω",
	"varGamma":   "Γ",
	"varDelta":   "Δ",
	"varTheta":   "Θ",
	"varLambda":  "Λ",
	"varXi":      "Ξ",
	"varPi":      "Π",
	"varSigma":   "Σ",
	"varUpsilon": "Υ",
	"varPhi":     "Φ",
	"varPsi":     "Ψ",
	"varOmega":   "Ω",
	"varkappa":   "ϰ",

	// Calculus, big operators, and special symbols
	"infty":     "∞",
	"partial":   "∂",
	"nabla":     "∇",
	"hbar":      "ℏ",
	"ell":       "ℓ",
	"aleph":     "ℵ",
	"wp":        "℘",
	"Re":        "ℜ",
	"Im":        "ℑ",
	"sum":       "∑",
	"prod":      "∏",
	"coprod":    "∐",
	"int":       "∫",
	"iint":      "∬",
	"iiint":     "∭",
	"oint":      "∮",
	"oiint":     "∯",
	"oiiint":    "∰",
	"bigcap":    "⋂",
	"bigcup":    "⋃",
	"bigoplus":  "⨁",
	"bigotimes": "⨂",
	"bigvee":    "⋁",
	"bigwedge":  "⋀",

	// Binary operators
	"pm":       "±",
	"mp":       "∓",
	"times":    "×",
	"div":      "÷",
	"cdot":     "·",
	"ast":      "∗",
	"star":     "⋆",
	"circ":     "∘",
	"bullet":   "•",
	"oplus":    "⊕",
	"ominus":   "⊖",
	"otimes":   "⊗",
	"odot":     "⊙",
	"cup":      "∪",
	"cap":      "∩",
	"setminus": "∖",
	"wedge":    "∧",
	"vee":      "∨",
	"land":     "∧",
	"lor":      "∨",
	"lnot":     "¬",
	"neg":      "¬",
	"dagger":   "†",
	"ddagger":  "‡",
	"amalg":    "⨿",
	"wr":       "≀",

	// Relations
	"le":         "≤",
	"leq":        "≤",
	"leqslant":   "≤",
	"ge":         "≥",
	"geq":        "≥",
	"geqslant":   "≥",
	"neq":        "≠",
	"ne":         "≠",
	"approx":     "≈",
	"equiv":      "≡",
	"sim":        "∼",
	"simeq":      "≃",
	"cong":       "≅",
	"propto":     "∝",
	"ll":         "≪",
	"gg":         "≫",
	"subset":     "⊂",
	"subseteq":   "⊆",
	"supset":     "⊃",
	"supseteq":   "⊇",
	"nsubseteq":  "⊈",
	"in":         "∈",
	"notin":      "∉",
	"ni":         "∋",
	"emptyset":   "∅",
	"varnothing": "∅",
	"forall":     "∀",
	"exists":     "∃",
	"nexists":    "∄",
	"top":        "⊤",
	"bot":        "⊥",
	"perp":       "⊥",
	"parallel":   "∥",
	"mid":        "∣",
	"nmid":       "∤",
	"vdash":      "⊢",
	"dashv":      "⊣",
	"models":     "⊨",
	"prec":       "≺",
	"succ":       "≻",
	"preceq":     "⪯",
	"succeq":     "⪰",
	"triangleq":  "≜",
	"coloneqq":   "≔",
	"eqqcolon":   "≕",
	"doteq":      "≐",

	// Arrows
	"to":                 "→",
	"rightarrow":         "→",
	"longrightarrow":     "→",
	"leftarrow":          "←",
	"longleftarrow":      "←",
	"leftrightarrow":     "↔",
	"longleftrightarrow": "↔",
	"Rightarrow":         "⇒",
	"Longrightarrow":     "⇒",
	"implies":            "⇒",
	"Leftarrow":          "⇐",
	"Longleftarrow":      "⇐",
	"Leftrightarrow":     "⇔",
	"Longleftrightarrow": "⇔",
	"iff":                "⇔",
	"mapsto":             "↦",
	"longmapsto":         "↦",
	"uparrow":            "↑",
	"downarrow":          "↓",
	"updownarrow":        "↕",
	"Uparrow":            "⇑",
	"Downarrow":          "⇓",
	"nearrow":            "↗",
	"searrow":            "↘",
	"swarrow":            "↙",
	"nwarrow":            "↖",
	"hookrightarrow":     "↪",
	"hookleftarrow":      "↩",
	"twoheadrightarrow":  "↠",
	"twoheadleftarrow":   "↞",

	// Geometry, delimiters, dots, and misc
	"angle":             "∠",
	"measuredangle":     "∡",
	"triangle":          "△",
	"square":            "□",
	"blacksquare":       "■",
	"diamond":           "◇",
	"degree":            "°",
	"prime":             "′",
	"backprime":         "‵",
	"dots":              "…",
	"ldots":             "…",
	"cdots":             "⋯",
	"vdots":             "⋮",
	"ddots":             "⋱",
	"dotsb":             "⋯",
	"dotsc":             "…",
	"dotsi":             "⋯",
	"dotsm":             "⋯",
	"dotso":             "…",
	"cdotp":             "·",
	"ldotp":             ".",
	"therefore":         "∴",
	"because":           "∵",
	"checkmark":         "✓",
	"langle":            "⟨",
	"rangle":            "⟩",
	"lceil":             "⌈",
	"rceil":             "⌉",
	"lfloor":            "⌊",
	"rfloor":            "⌋",
	"lvert":             "|",
	"rvert":             "|",
	"lVert":             "‖",
	"rVert":             "‖",
	"Vert":              "‖",
	"vert":              "|",
	"backslash":         "\\",
	"smallsetminus":     "∖",
	"colon":             ":",
	"asymp":             "≍",
	"nequiv":            "≢",
	"nsim":              "≁",
	"ncong":             "≇",
	"nless":             "≮",
	"ngtr":              "≯",
	"nleq":              "≰",
	"ngeq":              "≱",
	"lesssim":           "≲",
	"gtrsim":            "≳",
	"lll":               "⋘",
	"ggg":               "⋙",
	"subsetneq":         "⊊",
	"supsetneq":         "⊋",
	"nsubset":           "⊄",
	"nsupset":           "⊅",
	"sqsubseteq":        "⊑",
	"sqsupseteq":        "⊒",
	"bowtie":            "⋈",
	"triangleleft":      "◁",
	"triangleright":     "▷",
	"trianglelefteq":    "⊴",
	"trianglerighteq":   "⊵",
	"impliedby":         "⇐",
	"gets":              "←",
	"leadsto":           "↝",
	"rightleftharpoons": "⇌",
	"imath":             "ı",
	"jmath":             "ȷ",
	"beth":              "ℶ",
	"gimel":             "ℷ",
	"daleth":            "ℸ",
	"eth":               "ð",
	"mho":               "℧",
	"dag":               "†",
	"ddag":              "‡",
}

// Named upright operators (rendered without italic slant, with clean spacing).
var latexOperators = map[string]string{
	"lim":     "lim",
	"limsup":  "lim sup",
	"liminf":  "lim inf",
	"sin":     "sin",
	"cos":     "cos",
	"tan":     "tan",
	"cot":     "cot",
	"sec":     "sec",
	"csc":     "csc",
	"arcsin":  "arcsin",
	"arccos":  "arccos",
	"arctan":  "arctan",
	"sinh":    "sinh",
	"cosh":    "cosh",
	"tanh":    "tanh",
	"log":     "log",
	"ln":      "ln",
	"lg":      "lg",
	"exp":     "exp",
	"det":     "det",
	"dim":     "dim",
	"ker":     "ker",
	"hom":     "hom",
	"deg":     "deg",
	"gcd":     "gcd",
	"lcm":     "lcm",
	"arg":     "arg",
	"min":     "min",
	"max":     "max",
	"sup":     "sup",
	"inf":     "inf",
	"Pr":      "Pr",
	"mod":     "mod",
	"bmod":    "mod",
	"pmod":    "mod",
	"argmin":  "argmin",
	"argmax":  "argmax",
	"softmax": "softmax",
	"tr":      "tr",
	"rank":    "rank",
	"Var":     "Var",
	"Cov":     "Cov",
	"sgn":     "sgn",
	"sign":    "sign",
	"diag":    "diag",
	"supp":    "supp",
	"erf":     "erf",
	"erfc":    "erfc",
	"arccot":  "arccot",
	"arcsec":  "arcsec",
	"arccsc":  "arccsc",
	"arsinh":  "arsinh",
	"arcosh":  "arcosh",
	"artanh":  "artanh",
	"coth":    "coth",
	"sech":    "sech",
	"csch":    "csch",
}

// Big operators that accept stacked upper/lower limits in display math.
var latexLimitOps = map[string]bool{
	"sum": true, "prod": true, "coprod": true,
	"int": true, "iint": true, "iiint": true, "oint": true, "oiint": true, "oiiint": true,
	"bigcap": true, "bigcup": true, "bigoplus": true, "bigotimes": true,
	"bigvee": true, "bigwedge": true,
	"lim": true, "limsup": true, "liminf": true,
	"min": true, "max": true, "sup": true, "inf": true,
	"argmin": true, "argmax": true,
}

// Operators treated as binary spacing operators in normal math prose.
var latexBinaryOps = map[string]bool{
	"=": true, "+": true, "-": true, "<": true, ">": true,
	"±": true, "∓": true, "×": true, "÷": true, "·": true,
	"≤": true, "≥": true, "≠": true, "≈": true, "≡": true,
	"∼": true, "≃": true, "≅": true, "∝": true, "≪": true, "≫": true,
	"→": true, "←": true, "↔": true, "⇒": true, "⇐": true, "⇔": true, "↦": true,
	"↑": true, "↓": true, "↕": true, "⇑": true, "⇓": true,
	"↗": true, "↘": true, "↙": true, "↖": true,
	"↪": true, "↩": true, "↠": true, "↞": true,
	"∈": true, "∉": true, "∋": true, "⊂": true, "⊆": true, "⊃": true, "⊇": true,
	"∪": true, "∩": true, "∖": true, "∧": true, "∨": true,
	"⊕": true, "⊖": true, "⊗": true, "⊙": true,
	"⊢": true, "⊨": true, "≜": true, "≔": true, "≕": true, "≐": true,
	"∣": true, "∥": true, "⊥": true, "≺": true, "≻": true, "⪯": true, "⪰": true,
	"≍": true, "≢": true, "≁": true, "≇": true, "≮": true, "≯": true, "≰": true, "≱": true,
	"≲": true, "≳": true, "⋘": true, "⋙": true, "⊊": true, "⊋": true, "⊄": true, "⊅": true,
	"⊑": true, "⊒": true, "⋈": true, "↝": true, "⇌": true, "∤": true, "≉": true,
}

// Unicode superscript character map.
var superscriptMap = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴',
	'5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾',
	'a': 'ᵃ', 'b': 'ᵇ', 'c': 'ᶜ', 'd': 'ᵈ', 'e': 'ᵉ',
	'f': 'ᶠ', 'g': 'ᵍ', 'h': 'ʰ', 'i': 'ⁱ', 'j': 'ʲ',
	'k': 'ᵏ', 'l': 'ˡ', 'm': 'ᵐ', 'n': 'ⁿ', 'o': 'ᵒ',
	'p': 'ᵖ', 'r': 'ʳ', 's': 'ˢ', 't': 'ᵗ', 'u': 'ᵘ',
	'v': 'ᵛ', 'w': 'ʷ', 'x': 'ˣ', 'y': 'ʸ', 'z': 'ᶻ',
	'A': 'ᴬ', 'B': 'ᴮ', 'D': 'ᴰ', 'E': 'ᴱ', 'G': 'ᴳ',
	'H': 'ᴴ', 'I': 'ᴵ', 'J': 'ᴶ', 'K': 'ᴷ', 'L': 'ᴸ',
	'M': 'ᴹ', 'N': 'ᴺ', 'O': 'ᴼ', 'P': 'ᴾ', 'R': 'ᴿ',
	'T': 'ᵀ', 'U': 'ᵁ', 'V': 'ⱽ', 'W': 'ᵂ',
	'α': 'ᵅ', 'β': 'ᵝ', 'γ': 'ᵞ', 'δ': 'ᵟ', 'ε': 'ᵋ',
	'θ': 'ᶿ', 'ι': 'ᶥ', 'φ': 'ᵠ', 'χ': 'ᵡ',
	'∗': '*', '′': '′', '∞': '∞',
}

// Unicode subscript character map.
var subscriptMap = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄',
	'5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎',
	'a': 'ₐ', 'e': 'ₑ', 'h': 'ₕ', 'i': 'ᵢ', 'j': 'ⱼ',
	'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ', 'o': 'ₒ',
	'p': 'ₚ', 'r': 'ᵣ', 's': 'ₛ', 't': 'ₜ', 'u': 'ᵤ',
	'v': 'ᵥ', 'x': 'ₓ',
	'β': 'ᵦ', 'γ': 'ᵧ', 'ρ': 'ᵨ', 'φ': 'ᵩ', 'χ': 'ᵪ',
}

// Common simple fractions with dedicated Unicode vulgar fraction glyphs (inline mode).
var vulgarFractions = map[string]string{
	"1/2": "½", "1/3": "⅓", "2/3": "⅔",
	"1/4": "¼", "3/4": "¾",
	"1/5": "⅕", "2/5": "⅖", "3/5": "⅗", "4/5": "⅘",
	"1/6": "⅙", "5/6": "⅚",
	"1/8": "⅛", "3/8": "⅜", "5/8": "⅝", "7/8": "⅞",
}

// Precomposed hat letters for universal monospace font crispness.
var precomposedHat = map[rune]rune{
	'a': 'â', 'e': 'ê', 'i': 'î', 'o': 'ô', 'u': 'û',
	'c': 'ĉ', 'g': 'ĝ', 'h': 'ĥ', 'j': 'ĵ', 's': 'ŝ', 'w': 'ŵ', 'y': 'ŷ',
	'A': 'Â', 'E': 'Ê', 'I': 'Î', 'O': 'Ô', 'U': 'Û',
	'C': 'Ĉ', 'G': 'Ĝ', 'H': 'Ĥ', 'J': 'Ĵ', 'S': 'Ŝ', 'W': 'Ŵ', 'Y': 'Ŷ',
}

var precomposedTilde = map[rune]rune{
	'a': 'ã', 'n': 'ñ', 'o': 'õ', 'i': 'ĩ', 'u': 'ũ',
	'A': 'Ã', 'N': 'Ñ', 'O': 'Õ', 'I': 'Ĩ', 'U': 'Ũ',
}

var precomposedBar = map[rune]rune{
	'a': 'ā', 'e': 'ē', 'i': 'ī', 'o': 'ō', 'u': 'ū', 'y': 'ȳ',
	'A': 'Ā', 'E': 'Ē', 'I': 'Ī', 'O': 'Ō', 'U': 'Ū', 'Y': 'Ȳ',
}

// Combining diacritical marks for LaTeX accents when no precomposed glyph exists.
var accentCombining = map[string]rune{
	"hat":       '\u0302',
	"widehat":   '\u0302',
	"tilde":     '\u0303',
	"widetilde": '\u0303',
	"bar":       '\u0304',
	"overline":  '\u0304',
	"vec":       '\u20D7',
	"dot":       '\u0307',
	"ddot":      '\u0308',
	"acute":     '\u0301',
	"grave":     '\u0300',
	"check":     '\u030C',
	"breve":     '\u0306',
	"underline": '\u0332',
}

// mapMathAlphabet maps ASCII letters/digits into Unicode mathematical alphabets
// (\mathbb, \mathcal, \mathscr, \mathbf, \mathfrak). Uses BMP letterlike
// code points where defined so terminal font coverage stays high.
func mapMathAlphabet(styleName, s string) string {
	var out []rune
	for _, r := range s {
		out = append(out, mapMathRune(styleName, r))
	}
	return string(out)
}

func mapMathRune(styleName string, r rune) rune {
	switch styleName {
	case "mathbb":
		switch r {
		case 'C':
			return 'ℂ'
		case 'H':
			return 'ℍ'
		case 'N':
			return 'ℕ'
		case 'P':
			return 'ℙ'
		case 'Q':
			return 'ℚ'
		case 'R':
			return 'ℝ'
		case 'Z':
			return 'ℤ'
		}
		if r >= 'A' && r <= 'Z' {
			return 0x1D538 + (r - 'A')
		}
		if r >= 'a' && r <= 'z' {
			return 0x1D552 + (r - 'a')
		}
		if r >= '0' && r <= '9' {
			return 0x1D7D8 + (r - '0')
		}
	case "mathcal", "mathscr":
		switch r {
		case 'B':
			return 'ℬ'
		case 'E':
			return 'ℰ'
		case 'F':
			return 'ℱ'
		case 'H':
			return 'ℋ'
		case 'I':
			return 'ℐ'
		case 'L':
			return 'ℒ'
		case 'M':
			return 'ℳ'
		case 'R':
			return 'ℛ'
		case 'e':
			return 'ℯ'
		case 'g':
			return 'ℊ'
		case 'o':
			return 'ℴ'
		}
		if r >= 'A' && r <= 'Z' {
			return 0x1D49C + (r - 'A')
		}
		if r >= 'a' && r <= 'z' {
			return 0x1D4B6 + (r - 'a')
		}
	case "mathbf", "boldsymbol", "bm":
		if r >= 'A' && r <= 'Z' {
			return 0x1D400 + (r - 'A')
		}
		if r >= 'a' && r <= 'z' {
			return 0x1D41A + (r - 'a')
		}
		if r >= '0' && r <= '9' {
			return 0x1D7CE + (r - '0')
		}
	case "mathfrak":
		switch r {
		case 'C':
			return 'ℭ'
		case 'H':
			return 'ℌ'
		case 'I':
			return 'ℑ'
		case 'R':
			return 'ℜ'
		case 'Z':
			return 'ℨ'
		}
		if r >= 'A' && r <= 'Z' {
			return 0x1D504 + (r - 'A')
		}
		if r >= 'a' && r <= 'z' {
			return 0x1D51E + (r - 'a')
		}
	}
	return r
}

func applyLatexAccent(cmd, base string) string {
	runes := []rune(base)
	if len(runes) == 1 {
		r := runes[0]
		switch cmd {
		case "hat", "widehat":
			if p, ok := precomposedHat[r]; ok {
				return string(p)
			}
		case "tilde", "widetilde":
			if p, ok := precomposedTilde[r]; ok {
				return string(p)
			}
		case "bar", "overline":
			if p, ok := precomposedBar[r]; ok {
				return string(p)
			}
		}
	}
	comb, ok := accentCombining[cmd]
	if !ok || len(runes) == 0 {
		return base
	}
	// Apply combining mark across single glyph or short identifier; for multi-char
	// overline/hat on expressions, place mark cleanly on each non-space rune.
	if len(runes) == 1 {
		return string([]rune{runes[0], comb})
	}
	if cmd == "overline" || cmd == "underline" || cmd == "bar" {
		var out []rune
		for _, r := range runes {
			out = append(out, r)
			if r != ' ' {
				out = append(out, comb)
			}
		}
		return string(out)
	}
	return string([]rune{runes[0], comb}) + string(runes[1:])
}

// Supported standalone LaTeX display environments.
var latexDisplayEnvs = map[string]bool{
	"aligned": true, "align": true, "align*": true,
	"alignedat": true, "alignedat*": true, "alignat": true, "alignat*": true,
	"flalign": true, "flalign*": true,
	"equation": true, "equation*": true,
	"gather": true, "gather*": true,
	"multline": true, "multline*": true,
	"split": true, "cases": true,
	"matrix": true, "pmatrix": true, "bmatrix": true,
	"Bmatrix": true, "vmatrix": true, "Vmatrix": true,
	"smallmatrix": true, "array": true,
}
