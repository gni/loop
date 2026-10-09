package render

import (
	"bytes"
	"io"
	"strings"

	"github.com/alecthomas/chroma/v2/quick"
	"loop/pkg/ui/style"
)

// HighlightWithoutTrailingNewline highlights the source using chroma, stripping any
// ANSI sequences first and ensuring single-line inputs do not acquire trailing newlines.
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
	if chromaStyle == "" {
		chromaStyle = "friendly"
	}
	var buf bytes.Buffer
	err := quick.Highlight(&buf, source, lang, "terminal16", chromaStyle)
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
