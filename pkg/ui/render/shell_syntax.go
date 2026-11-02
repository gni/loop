package render

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
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
// filename-based syntax language and whether a file-reading command is present.
func shellCommandSyntaxHint(command string) (fileLang string, readCommand bool) {
	reading, commandNext, redirect := false, true, false
	for _, word := range shellSyntaxWords(command) {
		if len(word) == 1 && strings.ContainsAny(word, "\n|&;()") {
			reading, commandNext, redirect = false, true, false
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
			if word == "command" || word == "env" || (strings.Contains(word, "=") && !strings.HasPrefix(word, "-")) {
				continue
			}
			commandNext = false
			switch filepath.Base(word) {
			case "cat", "head", "tail", "sed", "awk", "nl", "bat", "batcat", "grep", "rg":
				reading, readCommand = true, true
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
			return "plaintext", readCommand
		}
		fileLang = candidate
	}
	return fileLang, readCommand
}

// ShellOutputLanguage favors the source filenames of common read commands.
// Unrelated commands retain plain logs, while recognizable JSON is highlighted.
func ShellOutputLanguage(command, source string) string {
	language, readCommand := shellCommandSyntaxHint(command)
	if language != "" {
		return language
	}
	if json.Valid([]byte(source)) && (strings.HasPrefix(strings.TrimSpace(source), "{") || strings.HasPrefix(strings.TrimSpace(source), "[")) {
		return "json"
	}
	if readCommand {
		return ResolveSyntaxLanguage("", source)
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
	if chromaStyle == "" || chromaStyle == "bw" {
		chromaStyle = "friendly"
	}
	var colored bytes.Buffer
	if err := formatters.Get("terminal256").Format(&colored, styles.Get(chromaStyle), clipped); err != nil {
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
	command, chromaStyle, prefix string
	resolvedLang                 string
	readCommand                  bool
	sampledBytes                 int
	nativeColor                  bool
}

// NewShellOutputWriter colors source printed by read commands while preserving
// partial lines, original program colors, and concurrent stdout/stderr writes.
func NewShellOutputWriter(w io.Writer, command string, theme style.UITheme) io.Writer {
	fileLang, readCmd := shellCommandSyntaxHint(command)
	return &shellSyntaxWriter{
		writer:       w,
		command:      command,
		chromaStyle:  theme.ChromaStyle,
		resolvedLang: fileLang,
		readCommand:  readCmd,
	}
}

func (w *shellSyntaxWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for written < len(p) {
		end := min(written+4096, len(p))
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
	if !w.readCommand {
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			w.resolvedLang = "plaintext"
			return "plaintext"
		}
		if json.Valid([]byte(sample)) {
			w.resolvedLang = "json"
			return "json"
		}
		if w.sampledBytes >= 4096 {
			w.resolvedLang = "plaintext"
		}
		return "plaintext"
	}
	if json.Valid([]byte(sample)) && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		w.resolvedLang = "json"
		return "json"
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
	if w.resolvedLang == "plaintext" {
		return w.writer.Write(p)
	}
	source := string(p)
	w.sampledBytes += len(p)
	combined := w.prefix + source
	language := w.resolveStreamLanguage(combined)
	colored := highlightSyntaxFragment(w.prefix, source, language, w.chromaStyle)
	n, err := io.WriteString(w.writer, colored)
	if err != nil {
		return 0, err
	}
	if n != len(colored) {
		return 0, io.ErrShortWrite
	}
	if w.resolvedLang == "plaintext" {
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

// NewShellOutputWriter adds presentation colors without altering captured tool
// results. Its caller serializes stdout and stderr before writing here.
func (sr *StreamRenderer) NewShellOutputWriter(w io.Writer, arguments string) io.Writer {
	sr.mu.Lock()
	theme := sr.theme
	sr.mu.Unlock()
	return NewShellOutputWriter(w, ExtractToolTarget("bash", arguments), theme)
}
