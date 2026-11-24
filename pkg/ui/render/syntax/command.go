package syntax

import (
	"path/filepath"
	"strings"
	"unicode"

	"loop/pkg/ui/style"
)

// BashCommandHighlighter highlights shell commands line-by-line while detecting
// embedded heredocs (such as `python3 - <<'PY'`, `node - <<'JS'`, or
// `cat > file.yaml <<'EOF'`) and applying the embedded language's Chroma lexer
// inside the heredoc body.
type BashCommandHighlighter struct {
	theme            style.UITheme
	bashPrefix       string
	inHeredoc        bool
	heredocDelim     string
	heredocStripTabs bool
	heredocLang      string
	heredocPrefix    string
}

// NewBashCommandHighlighter creates a stateful line-by-line highlighter for
// bash commands and embedded heredoc scripts.
func NewBashCommandHighlighter(theme style.UITheme) *BashCommandHighlighter {
	return &BashCommandHighlighter{theme: theme}
}

// SetTheme updates the theme used for subsequent lines.
func (h *BashCommandHighlighter) SetTheme(theme style.UITheme) {
	if h != nil {
		h.theme = theme
	}
}

// HighlightLine highlights one cleaned command line without adding a newline.
func (h *BashCommandHighlighter) HighlightLine(line string) string {
	if line == "" {
		if h.inHeredoc {
			h.heredocPrefix = syntaxContextTail(h.heredocPrefix + "\n")
		} else {
			h.bashPrefix = syntaxContextTail(h.bashPrefix + "\n")
		}
		return ""
	}

	if h.inHeredoc {
		candidate := line
		if h.heredocStripTabs {
			candidate = strings.TrimLeft(line, "\t")
		}
		if candidate == h.heredocDelim {
			h.inHeredoc = false
			h.heredocDelim = ""
			h.heredocStripTabs = false
			h.heredocLang = ""
			h.heredocPrefix = ""
			return style.NewStyle().Foreground(h.theme.TextMuted).Render(line)
		}

		lang := h.heredocLang
		if lang == "" || lang == "plaintext" {
			if inferred := inferHeredocBodyLineLang(line); inferred != "" {
				lang = inferred
				h.heredocLang = inferred
			}
		}

		colored := line
		if lang != "" && lang != "plaintext" {
			colored = highlightSyntaxFragment(h.heredocPrefix, line, lang, h.theme.ChromaStyle)
		}
		h.heredocPrefix = syntaxContextTail(h.heredocPrefix + line + "\n")
		if !strings.Contains(colored, "\x1b[") {
			return style.NewStyle().Foreground(h.theme.Text).Render(line)
		}
		return colored
	}

	colored := highlightSyntaxFragment(h.bashPrefix, line, "bash", h.theme.ChromaStyle)
	h.bashPrefix = syntaxContextTail(h.bashPrefix + line + "\n")

	if delim, stripTabs, lang, ok := detectHeredocStart(line); ok {
		h.inHeredoc = true
		h.heredocDelim = delim
		h.heredocStripTabs = stripTabs
		h.heredocLang = lang
		h.heredocPrefix = ""
		h.bashPrefix = ""
	}

	if !strings.Contains(colored, "\x1b[") {
		return style.NewStyle().Foreground(h.theme.Text).Render(line)
	}
	return colored
}

// HighlightBashCommandLines highlights a complete bash command string into
// per-line ANSI-styled strings matching the input line count 1:1.
func HighlightBashCommandLines(lines []string, theme style.UITheme) []string {
	h := NewBashCommandHighlighter(theme)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = h.HighlightLine(line)
	}
	return out
}

func inferHeredocBodyLineLang(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "#!") {
		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "python"):
			return "python"
		case strings.Contains(lower, "node"), strings.Contains(lower, "deno"), strings.Contains(lower, "bun"):
			return "javascript"
		case strings.Contains(lower, "ruby"):
			return "ruby"
		case strings.Contains(lower, "perl"):
			return "perl"
		case strings.Contains(lower, "bash"), strings.Contains(lower, "/sh"), strings.Contains(lower, "zsh"):
			return "bash"
		}
	}
	if looksLikeJSONPrefix(trimmed) {
		return "json"
	}
	if strings.HasPrefix(trimmed, "diff --git ") {
		return "diff"
	}
	return ""
}

// detectHeredocStart scans a single shell command line (outside quotes) for a
// heredoc operator (<< or <<-) and infers the embedded language from the
// invoking command, redirect target filename, or heredoc delimiter tag.
func detectHeredocStart(line string) (delim string, stripTabs bool, lang string, ok bool) {
	var quote rune
	escaped := false
	arithDepth := 0

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if rune(ch) == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = rune(ch)
			continue
		}
		if ch == '#' && (i == 0 || unicode.IsSpace(rune(line[i-1]))) {
			break
		}
		if ch == '(' && i+1 < len(line) && line[i+1] == '(' {
			arithDepth++
			i++
			continue
		}
		if ch == ')' && arithDepth > 0 && i+1 < len(line) && line[i+1] == ')' {
			arithDepth--
			i++
			continue
		}
		if arithDepth > 0 {
			continue
		}
		if ch == '<' && i+1 < len(line) && line[i+1] == '<' {
			// Skip here-strings (<<<).
			if i+2 < len(line) && line[i+2] == '<' {
				i += 2
				continue
			}
			j := i + 2
			st := false
			if j < len(line) && line[j] == '-' {
				st = true
				j++
			}
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			parsed, endIdx := parseHeredocDelimiter(line, j)
			if parsed == "" {
				continue
			}
			// Ignore bit-shift-like numeric operands when unquoted.
			quoted := j < len(line) && (line[j] == '\'' || line[j] == '"' || line[j] == '\\')
			if !quoted && !isHeredocIdentStart(parsed[0]) {
				continue
			}
			lang := inferHeredocLanguage(line[:i], line[endIdx:], parsed)
			return parsed, st, lang, true
		}
	}
	return "", false, "", false
}

func isHeredocIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func parseHeredocDelimiter(line string, start int) (string, int) {
	if start >= len(line) {
		return "", start
	}
	var sb strings.Builder
	i := start
	for i < len(line) {
		ch := line[i]
		if ch == '\'' || ch == '"' {
			q := ch
			i++
			for i < len(line) && line[i] != q {
				sb.WriteByte(line[i])
				i++
			}
			if i >= len(line) {
				return "", start
			}
			i++
			continue
		}
		if ch == '\\' && i+1 < len(line) {
			i++
			sb.WriteByte(line[i])
			i++
			continue
		}
		if unicode.IsSpace(rune(ch)) || strings.ContainsRune("|&;()<>", rune(ch)) {
			break
		}
		sb.WriteByte(ch)
		i++
	}
	return sb.String(), i
}

func inferHeredocLanguage(before, after, delimiter string) string {
	// Extract the command segment immediately preceding << (after last &&, ||, ;, |, (, \n).
	words := shellSyntaxWords(before)
	segStart := 0
	for i, w := range words {
		if len(w) == 1 && strings.ContainsAny(w, "\n|&;()") {
			segStart = i + 1
		}
	}
	seg := words[segStart:]

	// Find the command executable and any output redirect target in this command.
	cmd := ""
	redirectTarget := ""
	expectRedirect := false
	skipNextArg := false
	for i := 0; i < len(seg); i++ {
		w := seg[i]
		if w == ">" {
			expectRedirect = true
			continue
		}
		if w == "<" {
			continue
		}
		if expectRedirect {
			redirectTarget = w
			expectRedirect = false
			continue
		}
		if cmd == "" {
			if w == "command" || w == "env" || w == "sudo" || w == "time" || w == "nohup" ||
				(strings.Contains(w, "=") && !strings.HasPrefix(w, "-")) {
				continue
			}
			if w == "timeout" {
				skipNextArg = true
				continue
			}
			if skipNextArg {
				skipNextArg = false
				continue
			}
			cmd = strings.ToLower(filepath.Base(w))
			continue
		}
		// Unwrap wrapper runners like `uv run python3`, `poetry run python`, `npx tsx`.
		if (cmd == "uv" || cmd == "poetry" || cmd == "pipenv" || cmd == "pdm" || cmd == "cargo" || cmd == "docker" || cmd == "kubectl") && w == "run" {
			cmd = ""
			continue
		}
		if cmd == "npx" || cmd == "bunx" || cmd == "pnpx" {
			if !strings.HasPrefix(w, "-") {
				cmd = strings.ToLower(filepath.Base(w))
			}
			continue
		}
		if (cmd == "tee" || cmd == "cp") && !strings.HasPrefix(w, "-") && redirectTarget == "" {
			redirectTarget = w
		}
	}

	// Also check redirect targets appearing after the heredoc operator (e.g. `cat <<'EOF' > app.py`).
	if redirectTarget == "" && strings.TrimSpace(after) != "" {
		afterWords := shellSyntaxWords(after)
		expect := false
		for _, w := range afterWords {
			if len(w) == 1 && strings.ContainsAny(w, "\n|&;()") {
				break
			}
			if w == ">" {
				expect = true
				continue
			}
			if expect {
				redirectTarget = w
				break
			}
		}
	}

	if redirectTarget != "" {
		if resolved := ResolveSyntaxLanguage(redirectTarget, ""); resolved != "" && resolved != "plaintext" {
			return resolved
		}
	}

	if lang := langForCommandInterpreter(cmd, delimiter); lang != "" {
		return lang
	}
	return langForHeredocTag(delimiter)
}

func langForCommandInterpreter(cmd, delimiter string) string {
	switch cmd {
	case "python", "python3", "python2", "pypy", "pypy3", "ipython", "pytest":
		return "python"
	case "node", "nodejs", "bun", "deno":
		if tag := langForHeredocTag(delimiter); tag == "typescript" {
			return "typescript"
		}
		return "javascript"
	case "ts-node", "tsx":
		return "typescript"
	case "ruby", "irb":
		return "ruby"
	case "perl":
		return "perl"
	case "php":
		return "php"
	case "lua", "luajit":
		return "lua"
	case "r", "rscript":
		return "r"
	case "jq":
		return "json"
	case "yq":
		return "yaml"
	case "sqlite3", "psql", "mysql", "mariadb", "duckdb", "clickhouse-client":
		return "sql"
	case "bash", "sh", "zsh", "dash", "ksh", "fish":
		return "bash"
	case "awk", "gawk", "mawk":
		return "awk"
	case "sed":
		return "sed"
	case "go":
		return "go"
	case "rustc":
		return "rust"
	case "gcc", "clang", "cc":
		return "c"
	case "g++", "clang++", "c++":
		return "cpp"
	}
	return ""
}

func langForHeredocTag(tag string) string {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "py", "py3", "python", "python3":
		return "python"
	case "js", "mjs", "cjs", "javascript", "node":
		return "javascript"
	case "ts", "tsx", "typescript":
		return "typescript"
	case "json", "jsonc":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "sql":
		return "sql"
	case "sh", "bash", "zsh", "shell":
		return "bash"
	case "go", "golang":
		return "go"
	case "rs", "rust":
		return "rust"
	case "rb", "ruby":
		return "ruby"
	case "pl", "perl":
		return "perl"
	case "php":
		return "php"
	case "lua":
		return "lua"
	case "html", "htm":
		return "html"
	case "css", "scss":
		return "css"
	case "xml", "svg":
		return "xml"
	case "md", "markdown":
		return "markdown"
	case "diff", "patch":
		return "diff"
	case "c", "h":
		return "c"
	case "cpp", "cxx", "cc":
		return "cpp"
	case "java":
		return "java"
	case "dockerfile":
		return "docker"
	case "makefile", "make":
		return "makefile"
	case "ini", "cfg", "conf":
		return "ini"
	}
	return ""
}
