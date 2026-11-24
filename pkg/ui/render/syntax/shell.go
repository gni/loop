package syntax

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v3"
	"github.com/alecthomas/chroma/v3/formatters"
	"github.com/alecthomas/chroma/v3/lexers"
	"github.com/alecthomas/chroma/v3/styles"

	"loop/pkg/ui/style"
)

type shellOutputMode uint8

const (
	shellModeAuto shellOutputMode = iota
	shellModeRead
	shellModeGrep
	shellModeDiff
)

// SplitBashExitCodeFooter separates the trailing "[Command exit code: N]" line
// appended by tool finalization from the actual command output so language and
// JSON detection operate on clean command output.
func SplitBashExitCodeFooter(output string) (body, footer string) {
	trimmed := strings.TrimRight(output, "\r\n")
	if !strings.HasSuffix(trimmed, "]") {
		return output, ""
	}
	lastNL := strings.LastIndexByte(trimmed, '\n')
	lastLine := trimmed
	if lastNL >= 0 {
		lastLine = trimmed[lastNL+1:]
	}
	if !isBashExitCodeLine(lastLine) {
		return output, ""
	}
	if lastNL < 0 {
		return "", lastLine
	}
	return strings.TrimRight(trimmed[:lastNL], "\r\n"), lastLine
}

func isBashExitCodeLine(line string) bool {
	rest, ok := strings.CutPrefix(line, "[Command exit code: ")
	if !ok {
		return false
	}
	codeStr, ok := strings.CutSuffix(rest, "]")
	if !ok || codeStr == "" {
		return false
	}
	_, err := strconv.Atoi(codeStr)
	return err == nil
}

// shellSyntaxWords only extracts display hints. It never expands or executes
// shell expressions, reads files, or changes the command sent to the executor.
func shellSyntaxWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, r := range command {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case strings.ContainsRune("|&;()<>", r) || r == '\n':
			flush()
			words = append(words, string(r))
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
		}
	}
	if quote != 0 || escaped {
		return nil
	}
	flush()
	return words
}

// shellCommandSyntaxHint parses the shell command once to determine any explicit
// language hint (filename, diff, json, yaml) and output mode (read, grep, diff).
func shellCommandSyntaxHint(command string) (fileLang string, mode shellOutputMode) {
	reading, commandNext, redirect := false, true, false
	currentCmd := ""
	gitSubcmd := ""
	for _, word := range shellSyntaxWords(command) {
		if len(word) == 1 && strings.ContainsAny(word, "\n|&;()") {
			reading, commandNext, redirect = false, true, false
			currentCmd, gitSubcmd = "", ""
			continue
		}
		if word == ">" {
			redirect = true
			continue
		}
		if word == "<" {
			continue
		}
		if redirect {
			continue
		}
		if commandNext {
			if word == "command" || word == "env" || word == "sudo" || (strings.Contains(word, "=") && !strings.HasPrefix(word, "-")) {
				continue
			}
			commandNext = false
			currentCmd = filepath.Base(word)
			switch currentCmd {
			case "cat", "head", "tail", "sed", "awk", "nl", "bat", "batcat", "less", "more":
				reading = true
				if mode == shellModeAuto {
					mode = shellModeRead
				}
			case "grep", "egrep", "fgrep", "rg", "ag", "ack":
				reading = true
				mode = shellModeGrep
			case "diff", "colordiff":
				mode = shellModeDiff
				if fileLang == "" {
					fileLang = "diff"
				}
			case "jq":
				if fileLang == "" {
					fileLang = "json"
				}
			case "yq":
				if fileLang == "" {
					fileLang = "yaml"
				}
			}
			continue
		}

		if currentCmd == "git" {
			if gitSubcmd == "" {
				if strings.HasPrefix(word, "-") {
					continue
				}
				gitSubcmd = word
				switch gitSubcmd {
				case "diff":
					mode = shellModeDiff
					fileLang = "diff"
				case "show":
					mode = shellModeDiff
					fileLang = "diff"
				case "grep":
					mode = shellModeGrep
					reading = true
				}
				continue
			}
			switch gitSubcmd {
			case "log":
				if word == "-p" || word == "--patch" || word == "-u" {
					mode = shellModeDiff
					fileLang = "diff"
				}
			case "show":
				// Handle `git show HEAD:path/to/file.go` -> highlight as Go, not diff.
				if !strings.HasPrefix(word, "-") {
					if _, pathPart, ok := strings.Cut(word, ":"); ok && pathPart != "" {
						if candidate := ResolveSyntaxLanguage(pathPart, ""); candidate != "" && candidate != "plaintext" {
							fileLang = candidate
							mode = shellModeRead
						}
					}
				}
			}
			continue
		}

		if !reading || strings.HasPrefix(word, "-") || strings.ContainsAny(word, "$`*?") {
			continue
		}
		base := strings.ToLower(filepath.Base(word))
		if filepath.Ext(base) == "" && base != "dockerfile" && base != "makefile" {
			continue
		}
		candidate := ResolveSyntaxLanguage(word, "")
		if candidate == "plaintext" {
			continue
		}
		if fileLang != "" && fileLang != candidate {
			if mode == shellModeGrep {
				fileLang = ""
				continue
			}
			return "plaintext", mode
		}
		fileLang = candidate
	}
	return fileLang, mode
}

// IsBashGrepCommand reports whether command invokes a grep-style search tool
// (grep, rg, git grep, etc.) whose results use path:line:code formatting.
func IsBashGrepCommand(command string) bool {
	_, mode := shellCommandSyntaxHint(command)
	return mode == shellModeGrep
}

// looksLikeJSONPrefix recognizes both complete JSON documents and streaming
// JSON prefixes (before the closing brace arrives) without matching log tags
// such as "[INFO]" or "[Command exit code: 0]".
func looksLikeJSONPrefix(sample string) bool {
	trimmed := strings.TrimSpace(sample)
	if len(trimmed) < 2 {
		return false
	}
	first := trimmed[0]
	if first != '{' && first != '[' {
		return false
	}
	if json.Valid([]byte(trimmed)) {
		return true
	}
	rest := strings.TrimLeftFunc(trimmed[1:], unicode.IsSpace)
	if rest == "" {
		return false
	}
	if first == '{' {
		if rest[0] == '}' {
			return true
		}
		if rest[0] != '"' {
			return false
		}
		// Require a closing quote followed by ':' (or end of chunk inside the key).
		escaped := false
		for i := 1; i < len(rest); i++ {
			if escaped {
				escaped = false
				continue
			}
			if rest[i] == '\\' {
				escaped = true
				continue
			}
			if rest[i] == '"' {
				afterKey := strings.TrimLeftFunc(rest[i+1:], unicode.IsSpace)
				return afterKey == "" || afterKey[0] == ':'
			}
			if rest[i] == '\n' || rest[i] == '\r' {
				return false
			}
		}
		return true
	}
	// Array '[': accept nested object/array, quoted string, or JSON literal/number.
	switch rest[0] {
	case '{', '[', ']', '"':
		return true
	case 't':
		return strings.HasPrefix(rest, "true") && (len(rest) == 4 || isJSONDelim(rest[4]))
	case 'f':
		return strings.HasPrefix(rest, "false") && (len(rest) == 5 || isJSONDelim(rest[5]))
	case 'n':
		return strings.HasPrefix(rest, "null") && (len(rest) == 4 || isJSONDelim(rest[4]))
	}
	if rest[0] == '-' || (rest[0] >= '0' && rest[0] <= '9') {
		i := 0
		if rest[i] == '-' {
			i++
		}
		digits := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
			digits++
		}
		if digits == 0 {
			return false
		}
		if i < len(rest) && rest[i] == '.' {
			i++
			for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
				i++
			}
		}
		return i == len(rest) || isJSONDelim(rest[i])
	}
	return false
}

func isJSONDelim(b byte) bool {
	return b == ',' || b == ']' || b == '}' || b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func looksLikeUnifiedDiff(sample string) bool {
	trimmed := strings.TrimLeft(sample, "\r\n")
	if strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "@@ -") {
		return true
	}
	if strings.HasPrefix(trimmed, "--- ") {
		if _, rest, ok := strings.Cut(trimmed, "\n"); ok {
			return strings.HasPrefix(rest, "+++ ")
		}
	}
	return false
}

// ShellOutputLanguage favors the source filenames and structured formats of
// common shell commands. Unrelated commands retain plain logs, while JSON and
// unified diffs are highlighted automatically.
func ShellOutputLanguage(command, source string) string {
	cleanSource, _ := SplitBashExitCodeFooter(source)
	language, mode := shellCommandSyntaxHint(command)
	if language != "" && mode != shellModeGrep {
		return language
	}
	if looksLikeJSONPrefix(cleanSource) {
		return "json"
	}
	if looksLikeUnifiedDiff(cleanSource) {
		return "diff"
	}
	if mode == shellModeRead {
		return ResolveSyntaxLanguage("", cleanSource)
	}
	if mode == shellModeGrep && language != "" && !LooksLikeBashGrepOutput(cleanSource) {
		return language
	}
	return "plaintext"
}

// highlightSyntaxFragment colors only the new bytes. A bounded prefix lets strings
// and comments span pipe reads without delaying partial lines or adding text.
func highlightSyntaxFragment(prefix, source, language, chromaStyle string) string {
	if language == "plaintext" || language == "" {
		return source
	}
	lexer := lexers.Get(language)
	if lexer == nil {
		return source
	}
	iterator, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", Nested: true}, prefix+source)
	if err != nil {
		return source
	}
	clipped := func(yield func(chroma.Token) bool) {
		skip, remaining := len(prefix), len(source)
		for token := range iterator {
			if skip >= len(token.Value) {
				skip -= len(token.Value)
				continue
			}
			token.Value = token.Value[skip:]
			skip = 0
			if len(token.Value) > remaining {
				token.Value = token.Value[:remaining]
			}
			remaining -= len(token.Value)
			if token.Value != "" && !yield(token) {
				return
			}
			if remaining == 0 {
				return
			}
		}
	}
	if chromaStyle == "" {
		chromaStyle = "github-dark"
	}
	formatterName := style.ChromaFormatterName()
	if formatterName == "" {
		return source
	}
	formatter := formatters.Get(formatterName)
	if formatter == nil {
		formatter = formatters.Get("terminal256")
	}
	var colored bytes.Buffer
	if err := formatter.Format(&colored, styles.Get(chromaStyle), clipped); err != nil {
		return source
	}
	// Lexers may normalize whitespace. Shell output must stay byte-for-byte
	// identical when terminal colors are removed, including CR and missing LF.
	if style.StripAnsi(colored.String()) != source {
		return source
	}
	return colored.String()
}

type shellSyntaxWriter struct {
	mu                           sync.Mutex
	writer                       io.Writer
	theme                        style.UITheme
	command, chromaStyle, prefix string
	resolvedLang                 string
	mode                         shellOutputMode
	grepActive                   bool
	sampledBytes                 int
	nativeColor                  bool
}

// NewShellOutputWriter colors source, diffs, grep matches, and JSON printed by
// shell commands while preserving partial lines, original program colors, and
// concurrent stdout/stderr writes.
func NewShellOutputWriter(w io.Writer, command string, theme style.UITheme) io.Writer {
	fileLang, mode := shellCommandSyntaxHint(command)
	resolved := fileLang
	if mode == shellModeGrep {
		// Wait for the first chunk so path:line:code uses grep highlighting
		// instead of mis-highlighting filenames as source tokens.
		resolved = ""
	}
	return &shellSyntaxWriter{
		writer:       w,
		theme:        theme,
		command:      command,
		chromaStyle:  theme.ChromaStyle,
		resolvedLang: resolved,
		mode:         mode,
	}
}

func (w *shellSyntaxWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for written < len(p) {
		end := min(written+4096, len(p))
		if end < len(p) {
			// Back off to a valid UTF-8 rune boundary so multi-byte characters
			// are never split across internal fragments.
			cut := end
			for cut > written && end-cut < utf8.UTFMax && !utf8.RuneStart(p[cut]) {
				cut--
			}
			if cut < end && cut > written && utf8.RuneStart(p[cut]) && !utf8.FullRune(p[cut:end]) {
				end = cut
			}
		}
		n, err := w.writeFragment(p[written:end])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (w *shellSyntaxWriter) resolveStreamLanguage(sample string) string {
	if w.resolvedLang != "" {
		return w.resolvedLang
	}
	trimmed := strings.TrimSpace(sample)
	if trimmed == "" {
		return "plaintext"
	}
	if w.mode == shellModeGrep || LooksLikeBashGrepOutput(sample) {
		if LooksLikeBashGrepOutput(sample) {
			w.grepActive = true
			return ""
		}
		if w.mode == shellModeGrep {
			fileLang, _ := shellCommandSyntaxHint(w.command)
			if fileLang != "" {
				w.resolvedLang = fileLang
				return fileLang
			}
			if w.sampledBytes >= 512 {
				w.resolvedLang = "plaintext"
			}
			return "plaintext"
		}
	}
	if looksLikeUnifiedDiff(sample) {
		w.resolvedLang = "diff"
		return "diff"
	}
	if looksLikeJSONPrefix(sample) {
		w.resolvedLang = "json"
		return "json"
	}
	if w.mode != shellModeRead {
		if w.sampledBytes < 256 {
			if trimmed == "{" || trimmed == "[" ||
				((strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && !strings.Contains(sample, "\n")) ||
				(strings.HasPrefix(trimmed, "--- ") && strings.Count(trimmed, "\n") == 0) {
				return "plaintext"
			}
		}
		w.resolvedLang = "plaintext"
		return "plaintext"
	}
	lang := ResolveSyntaxLanguage("", sample)
	if lang != "" && lang != "plaintext" && (w.sampledBytes >= 256 || strings.Contains(sample, "\n")) {
		w.resolvedLang = lang
		return lang
	}
	if w.sampledBytes >= 1024 {
		if lang == "" {
			lang = "plaintext"
		}
		w.resolvedLang = lang
	}
	return lang
}

func (w *shellSyntaxWriter) writeFragment(p []byte) (int, error) {
	if bytes.ContainsRune(p, '\x1b') {
		w.nativeColor = true
	}
	if w.nativeColor || !utf8.Valid(p) {
		w.prefix = ""
		return w.writer.Write(p)
	}
	if w.resolvedLang == "plaintext" && !w.grepActive {
		return w.writer.Write(p)
	}
	source := string(p)
	w.sampledBytes += len(p)
	combined := w.prefix + source
	var colored string
	if w.grepActive {
		colored = HighlightBashGrepBody(source, w.theme)
	} else {
		language := w.resolveStreamLanguage(combined)
		if w.grepActive {
			colored = HighlightBashGrepBody(source, w.theme)
		} else {
			colored = highlightSyntaxFragment(w.prefix, source, language, w.chromaStyle)
		}
	}
	n, err := io.WriteString(w.writer, colored)
	if err != nil {
		return 0, err
	}
	if n != len(colored) {
		return 0, io.ErrShortWrite
	}
	if w.resolvedLang == "plaintext" || w.grepActive {
		w.prefix = ""
	} else {
		w.prefix = syntaxContextTail(combined)
	}
	return len(p), nil
}

func syntaxContextTail(source string) string {
	const contextBytes = 4096
	if len(source) > contextBytes {
		start := len(source) - contextBytes
		for start < len(source) && !utf8.RuneStart(source[start]) {
			start++
		}
		return source[start:]
	}
	return source
}
