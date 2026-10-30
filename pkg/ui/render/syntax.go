package render

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v3/lexers"
	"github.com/alecthomas/chroma/v3/quick"
	"loop/pkg/ui/style"
)

// ResolveSyntaxLanguage selects the Chroma lexer used by both live tool previews
// and completed tool output. The filename takes precedence over content guesses.
func ResolveSyntaxLanguage(name, source string) string {
	name = strings.TrimSpace(name)
	if name != "" && !strings.EqualFold(name, "plaintext") {
		base := filepath.Base(name)
		if lexer := lexers.Match(base); lexer != nil && lexer != lexers.Fallback {
			return lexer.Config().Name
		}
		if lexer := lexers.Get(name); lexer != nil && lexer != lexers.Fallback {
			return lexer.Config().Name
		}
		if filepath.Ext(base) == "" {
			if lexer := lexers.Match("file." + strings.TrimPrefix(base, ".")); lexer != nil && lexer != lexers.Fallback {
				return lexer.Config().Name
			}
		}
	}
	// Source-only detection is a fallback for filenames unknown to Chroma.
	// Small fragments are often ambiguous, especially during streaming.
	if len(source) >= 80 {
		if lexer := lexers.Analyse(source); lexer != nil && lexer != lexers.Fallback {
			return lexer.Config().Name
		}
	}
	return "plaintext"
}

// HighlightWithoutTrailingNewline highlights source without adding a newline.
func HighlightWithoutTrailingNewline(w io.Writer, source, lang, chromaStyle string) error {
	if strings.Contains(source, "\x1b") {
		source = style.StripAnsi(source)
	}
	switch strings.ToLower(lang) {
	case "md":
		lang = "markdown"
	case "yml":
		lang = "yaml"
	case "js":
		lang = "javascript"
	case "ts":
		lang = "typescript"
	case "py":
		lang = "python"
	case "sh":
		lang = "bash"
	}
	if chromaStyle == "" || chromaStyle == "bw" {
		chromaStyle = "friendly"
	}
	// Callers pass an explicit Chroma language name (or alias). Re-resolving
	// that name as if it were a filename can discard a valid lexer and turn
	// an already detected language into plaintext during live rendering.
	var buf bytes.Buffer
	err := quick.Highlight(&buf, source, lang, "terminal256", chromaStyle)
	if err != nil {
		buf.Reset()
		err = quick.Highlight(&buf, source, lang, "terminal16", chromaStyle)
	}
	if err != nil {
		_, writeErr := io.WriteString(w, source)
		return writeErr
	}
	data := buf.Bytes()
	if !strings.Contains(source, "\n") {
		var stripped []byte
		for _, b := range data {
			if b != '\n' && b != '\r' {
				stripped = append(stripped, b)
			}
		}
		data = stripped
	}
	_, err = w.Write(data)
	return err
}
