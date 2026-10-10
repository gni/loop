package stream

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// ScrollReplacer is an interface for writers that support replacing lines in scrollback.
type ScrollReplacer interface {
	ReplaceScrollLineBack(linesBack int, content string) bool
}

// JSONStreamParser incrementally parses streamed JSON tool calls.
type JSONStreamParser struct {
	InString    bool
	InEscape    bool
	CurrentKey  string
	InValue     bool
	Buf         strings.Builder
	IsContent   bool
	IsPath      bool
	PathPrinted bool

	// Fields for syntax highlighting streamed code
	Path       string
	LineBuffer strings.Builder

	// Field for language auto-detection
	GuessedLang string

	// Fields for path-in-title rendering
	ActiveToolName string
	TitlePrinted   bool

	// Output buffer for lazy rendering when path is not yet known
	OutputBuf strings.Builder

	NeedsLeadingNewline  bool
	ToolTitleLineNumbers []int
	ToolBodyStreamed     []bool
	ActiveToolIndex      int
	StreamWrites         bool
}

// NewJSONStreamParser creates a new streamed JSON tool parser.
func NewJSONStreamParser(streamWrites bool) *JSONStreamParser {
	return &JSONStreamParser{
		StreamWrites: streamWrites,
	}
}

func IsToolTargetPathKey(key string, toolName string) bool {
	if toolName == "todo" {
		return false
	}
	lower := strings.ToLower(key)
	switch lower {
	case "path", "file_path", "filepath", "file", "target", "target_file", "targetfile",
		"filename", "file_name", "write_path", "writepath", "absolutepath", "absolute_path",
		"searchpath", "search_path", "directorypath", "directory_path", "dirpath", "dir_path",
		"pattern", "query", "prompt", "name", "id", "task_id", "url", "uri", "destination", "dest", "outfile", "output_file":
		return true
	case "command", "commandline", "cmd", "script", "code", "input", "arguments", "args":
		return toolName == "bash" || toolName == "ls" || strings.Contains(toolName, "command") || strings.Contains(toolName, "exec") || strings.Contains(toolName, "run") || strings.Contains(toolName, "shell")
	}
	return false
}

func IsToolContentKey(key string, toolName string) bool {
	if IsToolTargetPathKey(key, toolName) {
		return false
	}
	lower := strings.ToLower(key)
	if toolName == "todo" {
		return lower == "task" || lower == "title" || lower == "description" || lower == "content"
	}
	if lower == "description" || lower == "summary" || lower == "explanation" || lower == "overwrite" || lower == "encoding" || lower == "background" {
		return false
	}
	if render.IsWriteLikeTool(toolName) {
		return true
	}
	return strings.Contains(lower, "content") || lower == "code" || lower == "body" || lower == "text" || lower == "data" || lower == "payload" || lower == "raw" || lower == "yaml" || lower == "yml" || lower == "json" || lower == "markdown" || lower == "md"
}

func (p *JSONStreamParser) NeedsPath() bool {
	return p.ActiveToolName == "read" || p.ActiveToolName == "write" || p.ActiveToolName == "edit" || p.ActiveToolName == "grep" || p.ActiveToolName == "find" || p.ActiveToolName == "bash" || p.ActiveToolName == "ls" || p.ActiveToolName == "list" || p.ActiveToolName == "create_subagent" || p.ActiveToolName == "spawn_subagent" || p.ActiveToolName == "load_skill" || p.ActiveToolName == "task_status" || p.ActiveToolName == "task_kill" || p.ActiveToolName == "list_subagents" || p.ActiveToolName == "audit_subagent" || strings.HasPrefix(p.ActiveToolName, "subagent__")
}

func DetectLangFromContent(content string) string {
	lines := strings.Split(content, "\n")
	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		if strings.HasPrefix(trimmed, "#!") {
			lower := strings.ToLower(trimmed)
			switch {
			case strings.Contains(lower, "python"):
				return "python"
			case strings.Contains(lower, "bash"), strings.Contains(lower, "sh"):
				return "bash"
			case strings.Contains(lower, "node"):
				return "javascript"
			case strings.Contains(lower, "ruby"):
				return "ruby"
			}
		}

		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
			if strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import ") {
				return "python"
			}
			if strings.HasPrefix(trimmed, "import ") {
				if strings.Contains(trimmed, " from '") || strings.Contains(trimmed, ` from "`) || strings.Contains(trimmed, "{") {
					return "typescript"
				}
				return "python"
			}
		}
		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "async def ") ||
			(strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":")) ||
			strings.HasPrefix(trimmed, "@pytest.") || strings.HasPrefix(trimmed, "@app.") || strings.HasPrefix(trimmed, "@router.") {
			return "python"
		}

		if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "func ") ||
			(strings.HasPrefix(trimmed, "type ") && (strings.HasSuffix(trimmed, "struct {") || strings.HasSuffix(trimmed, "interface {"))) {
			return "go"
		}

		if strings.HasPrefix(trimmed, "fn ") || strings.HasPrefix(trimmed, "pub fn ") ||
			strings.HasPrefix(trimmed, "use std::") || strings.HasPrefix(trimmed, "use crate::") ||
			strings.HasPrefix(trimmed, "pub struct ") || strings.HasPrefix(trimmed, "impl ") {
			return "rust"
		}

		if strings.HasPrefix(trimmed, "export default ") || strings.HasPrefix(trimmed, "export const ") ||
			strings.HasPrefix(trimmed, "export function ") || strings.HasPrefix(trimmed, "export interface ") ||
			strings.HasPrefix(trimmed, "export type ") || (strings.HasPrefix(trimmed, "const ") && strings.Contains(trimmed, "require(")) {
			return "typescript"
		}

		if strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<?xml") {
			return "html"
		}

		if strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "version:") || strings.HasPrefix(trimmed, "services:") {
			return "yaml"
		}

		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "json"
		}

		if strings.HasPrefix(trimmed, "#include <") || strings.HasPrefix(trimmed, `#include "`) {
			return "c"
		}

		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "CREATE TABLE ") || strings.HasPrefix(upper, "INSERT INTO ") {
			return "sql"
		}

		break
	}
	return ""
}

func DetectLangFromPath(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile":
		return "dockerfile"
	case "makefile":
		return "makefile"
	case "go.mod", "go.sum":
		return "go"
	case "cargo.toml", "cargo.lock":
		return "toml"
	case ".gitignore", ".env":
		return "bash"
	}
	ext := filepath.Ext(path)
	if len(ext) > 1 {
		return ext[1:]
	}
	return "plaintext"
}

func (p *JSONStreamParser) EmitLine(w io.Writer, theme style.UITheme) {
	line := p.LineBuffer.String()
	p.LineBuffer.Reset()

	if !p.TitlePrinted {
		p.PrintStreamTitle(w, theme)
		p.FlushOutputBuf(w, theme)
	}

	if p.GuessedLang == "" && p.Path != "" {
		p.GuessedLang = DetectLangFromPath(p.Path)
	}
	if p.GuessedLang == "" || p.GuessedLang == "plaintext" {
		if detected := DetectLangFromContent(line); detected != "" {
			p.GuessedLang = detected
		}
	}
	lang := p.GuessedLang
	if lang == "" {
		lang = "plaintext"
	}
	_ = render.HighlightWithoutTrailingNewline(w, line, lang, theme.ChromaStyle)
	fmt.Fprint(w, "\n")
}

func (p *JSONStreamParser) FlushOutputBuf(w io.Writer, theme style.UITheme) {
	if p.OutputBuf.Len() == 0 {
		return
	}
	raw := p.OutputBuf.String()
	p.OutputBuf.Reset()

	if p.GuessedLang == "" && p.Path != "" {
		p.GuessedLang = DetectLangFromPath(p.Path)
	}
	if p.GuessedLang == "" || p.GuessedLang == "plaintext" {
		if detected := DetectLangFromContent(raw); detected != "" {
			p.GuessedLang = detected
		}
	}
	lang := p.GuessedLang
	if lang == "" {
		lang = "plaintext"
	}

	lines := strings.Split(raw, "\n")
	for idx, l := range lines {
		if idx == len(lines)-1 && l == "" {
			break
		}
		_ = render.HighlightWithoutTrailingNewline(w, l, lang, theme.ChromaStyle)
		fmt.Fprint(w, "\n")
	}
}

func (p *JSONStreamParser) EmitContent(s string, w io.Writer, theme style.UITheme) {
	if !p.TitlePrinted {
		p.PrintStreamTitle(w, theme)
		p.FlushOutputBuf(w, theme)
	}

	fmt.Fprint(w, s)
}

func (p *JSONStreamParser) appendStringChunk(s string, w io.Writer, theme style.UITheme) {
	if p.InValue {
		if p.IsContent {
			if p.ActiveToolName == "todo" {
				p.EmitContent(s, w, theme)
			} else {
				if s == "\n" {
					p.EmitLine(w, theme)
				} else {
					p.LineBuffer.WriteString(s)
				}
			}
		} else if p.IsPath {
			p.Path += s
		}
	} else {
		p.Buf.WriteString(s)
	}
}

func (p *JSONStreamParser) Feed(chunk string, w io.Writer, theme style.UITheme) {
	for i := 0; i < len(chunk); i++ {
		char := chunk[i]

		if p.InString {
			if p.InEscape {
				p.InEscape = false
				var unescaped string
				switch char {
				case 'n':
					unescaped = "\n"
				case 't':
					unescaped = "\t"
				case 'r':
					unescaped = "\r"
				case '"', '\\', '/':
					unescaped = string(char)
				default:
					unescaped = "\\" + string(char)
				}

				p.appendStringChunk(unescaped, w, theme)
			} else if char == '\\' {
				p.InEscape = true
			} else if char == '"' {
				p.InString = false
				strVal := p.Buf.String()
				p.Buf.Reset()

				if !p.InValue {
					p.CurrentKey = strVal
				} else {
					if p.IsPath {
						if !p.TitlePrinted {
							p.PathPrinted = true
							if p.GuessedLang == "" && p.Path != "" {
								p.GuessedLang = DetectLangFromPath(p.Path)
							}
							p.PrintStreamTitle(w, theme)
							p.FlushOutputBuf(w, theme)
						} else if !p.PathPrinted {
							p.PathPrinted = true
							if p.GuessedLang == "" && p.Path != "" {
								p.GuessedLang = DetectLangFromPath(p.Path)
							}
							p.UpdateStreamTitleWithPath(w, theme)
						}
					}
					if p.ActiveToolName == "todo" && p.IsContent {
						fmt.Fprintln(w)
					}
					if p.IsContent {
						if p.LineBuffer.Len() > 0 {
							p.EmitLine(w, theme)
						}
					}
					p.InValue = false
					p.IsContent = false
					p.IsPath = false
				}
			} else {
				p.appendStringChunk(string(char), w, theme)
			}
		} else {
			if char == '"' {
				p.InString = true
			} else if char == '{' {
				p.InValue = false
			} else if char == ':' {
				p.InValue = true
				if IsToolTargetPathKey(p.CurrentKey, p.ActiveToolName) {
					if p.Path == "" {
						p.IsPath = true
					}
				} else if p.ActiveToolName != "edit" && IsToolContentKey(p.CurrentKey, p.ActiveToolName) {
					if p.StreamWrites {
						p.IsContent = true
						if p.ActiveToolName == "todo" && (p.CurrentKey == "task" || p.CurrentKey == "title" || p.CurrentKey == "description" || p.CurrentKey == "content") {
							fmt.Fprint(w, "  • ")
						}
						if p.GuessedLang == "" && p.Path != "" {
							p.GuessedLang = DetectLangFromPath(p.Path)
						}
						p.MarkBodyStreamed()
						if !p.TitlePrinted {
							if !p.NeedsPath() || p.Path != "" {
								p.PrintStreamTitle(w, theme)
								p.FlushOutputBuf(w, theme)
							}
						}
					}
				}
			} else if char == '}' || char == ']' {
				if p.IsContent && p.LineBuffer.Len() > 0 {
					p.EmitLine(w, theme)
				}
				if !p.TitlePrinted {
					p.PrintStreamTitle(w, theme)
					p.FlushOutputBuf(w, theme)
				}
				p.InValue = false
				p.IsContent = false
				p.IsPath = false
			} else if char == ',' {
				if p.IsContent && p.LineBuffer.Len() > 0 {
					p.EmitLine(w, theme)
				}
				p.InValue = false
				p.IsContent = false
				p.IsPath = false
			}
		}
	}
}

func (p *JSONStreamParser) PrintStreamTitle(w io.Writer, theme style.UITheme) {
	if p.TitlePrinted {
		return
	}
	if p.ActiveToolName == "bash" && strings.TrimSpace(p.Path) == "" {
		return
	}
	p.TitlePrinted = true
	p.EnsureTrackingIndex()

	if p.ActiveToolName == "bash" {
		p.ToolTitleLineNumbers[p.ActiveToolIndex] = GetNewlineCount(w)
		symbol := render.RenderToolSymbol(p.ActiveToolName, render.ToolStatusPending, theme)
		fmt.Fprintln(w, render.FormatBashCommandLine(symbol, p.Path, theme))
		return
	}

	p.ToolTitleLineNumbers[p.ActiveToolIndex] = GetNewlineCount(w)
	symbol := render.RenderToolSymbol(p.ActiveToolName, render.ToolStatusPending, theme)
	fmt.Fprintln(w, render.FormatToolTitle(symbol, p.ActiveToolName, p.Path, theme))
}

func (p *JSONStreamParser) UpdateStreamTitleWithPath(w io.Writer, theme style.UITheme) {
	p.EnsureTrackingIndex()
	line := p.ToolTitleLineNumbers[p.ActiveToolIndex]
	symbol := render.RenderToolSymbol(p.ActiveToolName, render.ToolStatusPending, theme)
	if p.ActiveToolName == "bash" {
		ReplaceTrackedStreamLine(w, line, render.FormatBashCommandLine(symbol, p.Path, theme))
		return
	}
	ReplaceTrackedStreamLine(w, line, render.FormatToolTitle(symbol, p.ActiveToolName, p.Path, theme))
}

func (p *JSONStreamParser) EnsureTrackingIndex() {
	for len(p.ToolTitleLineNumbers) <= p.ActiveToolIndex {
		p.ToolTitleLineNumbers = append(p.ToolTitleLineNumbers, -1)
	}
	for len(p.ToolBodyStreamed) <= p.ActiveToolIndex {
		p.ToolBodyStreamed = append(p.ToolBodyStreamed, false)
	}
}

func (p *JSONStreamParser) MarkBodyStreamed() {
	p.EnsureTrackingIndex()
	p.ToolBodyStreamed[p.ActiveToolIndex] = true
}

func GetNewlineCount(w io.Writer) int {
	for w != nil {
		if counter, ok := w.(interface{ GetCount() int }); ok {
			return counter.GetCount()
		}
		if bb, ok := w.(interface{ Bytes() []byte }); ok {
			return bytes.Count(bb.Bytes(), []byte{'\n'})
		}
		if unwrapper, ok := w.(interface{ Unwrap() io.Writer }); ok {
			if next := unwrapper.Unwrap(); next != nil && next != w {
				w = next
				continue
			}
		}
		break
	}
	return -1
}

func ReplaceTrackedStreamLine(w io.Writer, line int, content string) bool {
	if line < 0 {
		return false
	}
	currentLine := GetNewlineCount(w)
	if currentLine < line {
		return false
	}
	if replacer := findScrollReplacer(w); replacer != nil {
		return replacer.ReplaceScrollLineBack(currentLine-line, content)
	}

	for curr := w; curr != nil; {
		if buf, ok := curr.(interface {
			String() string
			Reset()
			WriteString(string) (int, error)
		}); ok {
			lines := strings.Split(buf.String(), "\n")
			if line >= 0 && line < len(lines) {
				lines[line] = content
				buf.Reset()
				buf.WriteString(strings.Join(lines, "\n"))
				return true
			}
			return false
		}
		if unwrapper, ok := curr.(interface{ Unwrap() io.Writer }); ok {
			curr = unwrapper.Unwrap()
		} else {
			break
		}
	}
	return false
}

func findScrollReplacer(w io.Writer) ScrollReplacer {
	for w != nil {
		if r, ok := w.(ScrollReplacer); ok {
			return r
		}
		if unwrapper, ok := w.(interface{ Unwrap() io.Writer }); ok {
			w = unwrapper.Unwrap()
		} else {
			break
		}
	}
	return nil
}
