package stream

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

type ScrollReplacer interface {
	ReplaceScrollLineBack(linesBack int, content string) bool
}

type JSONStreamParser struct {
	InString             bool
	InEscape             bool
	CurrentKey           string
	InValue              bool
	Buf                  strings.Builder
	IsContent            bool
	IsPath               bool
	PathPrinted          bool
	Path                 string
	LineBuffer           strings.Builder
	GuessedLang          string
	ActiveToolName       string
	TitlePrinted         bool
	OutputBuf            strings.Builder
	NeedsLeadingNewline  bool
	ToolTitleLineNumbers []int
	ToolTitlePrinted     []bool
	ToolBodyStreamed     []bool
	ActiveToolIndex      int
	StreamWrites         bool
	IsRawStream          bool
}

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
		return toolName == "bash" || toolName == "ls" || render.IsCommandLikeTool(toolName)
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
	switch lower {
	case "description", "summary", "explanation", "instructions", "instruction", "overwrite", "encoding", "background":
		return false
	}
	if render.IsWriteLikeTool(toolName) {
		return true
	}
	return strings.Contains(lower, "content") || lower == "code" || lower == "body" || lower == "text" || lower == "data" || lower == "payload" || lower == "raw" || lower == "yaml" || lower == "yml" || lower == "json" || lower == "markdown" || lower == "md"
}

var pathRequiredTools = map[string]bool{
	"read": true, "write": true, "edit": true, "grep": true, "find": true,
	"bash": true, "ls": true, "list": true, "create_subagent": true, "spawn_subagent": true,
	"load_skill": true, "task_status": true, "task_kill": true,
	"list_subagents": true, "audit_subagent": true,
}

func (p *JSONStreamParser) NeedsPath() bool {
	return pathRequiredTools[p.ActiveToolName] || strings.HasPrefix(p.ActiveToolName, "subagent__")
}

func DetectLangFromContent(content string) string {
	for _, rawLine := range strings.Split(content, "\n") {
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

		switch {
		case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
			return "python"
		case strings.HasPrefix(trimmed, "import "):
			if strings.Contains(trimmed, " from '") || strings.Contains(trimmed, ` from "`) || strings.Contains(trimmed, "{") {
				return "typescript"
			}
			return "python"
		case strings.HasPrefix(trimmed, "def "), strings.HasPrefix(trimmed, "async def "),
			(strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":")),
			strings.HasPrefix(trimmed, "@pytest."), strings.HasPrefix(trimmed, "@app."), strings.HasPrefix(trimmed, "@router."):
			return "python"
		case strings.HasPrefix(trimmed, "package "), strings.HasPrefix(trimmed, "func "),
			(strings.HasPrefix(trimmed, "type ") && (strings.HasSuffix(trimmed, "struct {") || strings.HasSuffix(trimmed, "interface {"))):
			return "go"
		case strings.HasPrefix(trimmed, "fn "), strings.HasPrefix(trimmed, "pub fn "),
			strings.HasPrefix(trimmed, "use std::"), strings.HasPrefix(trimmed, "use crate::"),
			strings.HasPrefix(trimmed, "pub struct "), strings.HasPrefix(trimmed, "impl "):
			return "rust"
		case strings.HasPrefix(trimmed, "export default "), strings.HasPrefix(trimmed, "export const "),
			strings.HasPrefix(trimmed, "export function "), strings.HasPrefix(trimmed, "export interface "),
			strings.HasPrefix(trimmed, "export type "), (strings.HasPrefix(trimmed, "const ") && strings.Contains(trimmed, "require(")):
			return "typescript"
		case strings.HasPrefix(trimmed, "<!DOCTYPE"), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<?xml"):
			return "html"
		case strings.HasPrefix(trimmed, "---"), strings.HasPrefix(trimmed, "version:"), strings.HasPrefix(trimmed, "services:"):
			return "yaml"
		case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
			return "json"
		case strings.HasPrefix(trimmed, "#include <"), strings.HasPrefix(trimmed, `#include "`):
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
	switch strings.ToLower(filepath.Base(path)) {
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
	if ext := filepath.Ext(path); len(ext) > 1 {
		return ext[1:]
	}
	return "plaintext"
}

func (p *JSONStreamParser) resolveLang(sample string) string {
	if p.GuessedLang == "" && p.Path != "" {
		p.GuessedLang = DetectLangFromPath(p.Path)
	}
	if p.GuessedLang == "" || p.GuessedLang == "plaintext" {
		if detected := DetectLangFromContent(sample); detected != "" {
			p.GuessedLang = detected
		}
	}
	if p.GuessedLang == "" {
		return "plaintext"
	}
	return p.GuessedLang
}

func (p *JSONStreamParser) EmitLine(w io.Writer, theme style.UITheme) {
	line := p.LineBuffer.String()
	p.LineBuffer.Reset()

	if !p.TitlePrinted {
		if p.NeedsPath() && strings.TrimSpace(p.Path) == "" {
			p.OutputBuf.WriteString(line + "\n")
			return
		}
		p.PrintStreamTitle(w, theme)
		p.FlushOutputBuf(w, theme)
	}

	_ = render.HighlightWithoutTrailingNewline(w, line, p.resolveLang(line), theme.ChromaStyle)
	fmt.Fprint(w, "\n")
}

func (p *JSONStreamParser) FlushOutputBuf(w io.Writer, theme style.UITheme) {
	if p.OutputBuf.Len() == 0 || (!p.TitlePrinted && p.NeedsPath() && strings.TrimSpace(p.Path) == "") {
		return
	}
	if !p.TitlePrinted && (!p.NeedsPath() || strings.TrimSpace(p.Path) != "") {
		p.PrintStreamTitle(w, theme)
	}
	raw := p.OutputBuf.String()
	p.OutputBuf.Reset()

	lang := p.resolveLang(raw)
	lines := strings.Split(raw, "\n")
	for idx, l := range lines {
		if idx == len(lines)-1 && l == "" {
			break
		}
		if p.ActiveToolName == "todo" {
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.Text).Render(l))
		} else {
			_ = render.HighlightWithoutTrailingNewline(w, l, lang, theme.ChromaStyle)
			fmt.Fprint(w, "\n")
		}
	}
}

func (p *JSONStreamParser) EmitContent(s string, w io.Writer, theme style.UITheme) {
	if !p.TitlePrinted {
		if p.NeedsPath() && strings.TrimSpace(p.Path) == "" {
			p.OutputBuf.WriteString(s)
			return
		}
		p.PrintStreamTitle(w, theme)
		p.FlushOutputBuf(w, theme)
	}
	fmt.Fprint(w, s)
}

func (p *JSONStreamParser) appendStringChunk(s string, w io.Writer, theme style.UITheme) {
	if !p.InValue {
		p.Buf.WriteString(s)
		return
	}
	if p.IsContent {
		if p.ActiveToolName == "todo" {
			p.EmitContent(s, w, theme)
		} else if s == "\n" {
			p.EmitLine(w, theme)
		} else {
			p.LineBuffer.WriteString(s)
		}
	} else if p.IsPath {
		p.Path += s
	}
}

func (p *JSONStreamParser) FeedRaw(chunk string, w io.Writer, theme style.UITheme) {
	p.EnsureTrackingIndex()
	p.IsContent = true
	p.MarkBodyStreamed()
	if !p.TitlePrinted && (!p.NeedsPath() || strings.TrimSpace(p.Path) != "") {
		p.PrintStreamTitle(w, theme)
		p.FlushOutputBuf(w, theme)
	}
	for i := 0; i < len(chunk); i++ {
		switch chunk[i] {
		case '\n':
			p.EmitLine(w, theme)
		case '\r':
		default:
			p.LineBuffer.WriteByte(chunk[i])
		}
	}
}

func (p *JSONStreamParser) Feed(chunk string, w io.Writer, theme style.UITheme) {
	for i := 0; i < len(chunk); {
		if p.InString {
			if p.InEscape {
				p.InEscape = false
				char := chunk[i]
				var unescaped string
				switch char {
				case 'n':
					unescaped = "\n"
					i++
				case 't':
					unescaped = "\t"
					i++
				case 'r':
					unescaped = "\r"
					i++
				case '"', '\\', '/':
					unescaped = string(char)
					i++
				case 'u':
					if i+5 <= len(chunk) {
						if val, err := strconv.ParseUint(chunk[i+1:i+5], 16, 32); err == nil {
							unescaped = string(rune(val))
							i += 5
						} else {
							unescaped = "\\u"
							i++
						}
					} else {
						unescaped = "\\u"
						i++
					}
				default:
					unescaped = "\\" + string(char)
					i++
				}
				p.appendStringChunk(unescaped, w, theme)
			} else if chunk[i] == '\\' {
				p.InEscape = true
				i++
			} else if chunk[i] == '"' {
				p.InString = false
				i++
				strVal := p.Buf.String()
				p.Buf.Reset()

				if !p.InValue {
					p.CurrentKey = strVal
				} else {
					if p.IsPath {
						if p.GuessedLang == "" && p.Path != "" {
							p.GuessedLang = DetectLangFromPath(p.Path)
						}
						if !p.TitlePrinted {
							p.PathPrinted = true
							p.PrintStreamTitle(w, theme)
							p.FlushOutputBuf(w, theme)
						} else if !p.PathPrinted {
							p.PathPrinted = true
							p.UpdateStreamTitleWithPath(w, theme)
						}
					}
					if p.ActiveToolName == "todo" && p.IsContent {
						if p.TitlePrinted {
							fmt.Fprintln(w)
						} else {
							p.OutputBuf.WriteString("\n")
						}
					}
					if p.IsContent && p.LineBuffer.Len() > 0 {
						p.EmitLine(w, theme)
					}
					p.InValue = false
					p.IsContent = false
					p.IsPath = false
				}
			} else {
				r, size := utf8.DecodeRuneInString(chunk[i:])
				if r != utf8.RuneError {
					p.appendStringChunk(chunk[i:i+size], w, theme)
					i += size
				} else {
					p.appendStringChunk(string(chunk[i]), w, theme)
					i++
				}
			}
		} else {
			switch chunk[i] {
			case '"':
				p.InString = true
			case '{':
				p.InValue = false
			case ':':
				p.InValue = true
				if IsToolTargetPathKey(p.CurrentKey, p.ActiveToolName) {
					if p.Path == "" {
						p.IsPath = true
					}
				} else if p.ActiveToolName != "edit" && IsToolContentKey(p.CurrentKey, p.ActiveToolName) && p.StreamWrites {
					p.IsContent = true
					if p.ActiveToolName == "todo" && (p.CurrentKey == "task" || p.CurrentKey == "title" || p.CurrentKey == "description" || p.CurrentKey == "content") {
						if p.TitlePrinted {
							fmt.Fprint(w, "  • ")
						} else {
							p.OutputBuf.WriteString("  • ")
						}
					}
					if p.GuessedLang == "" && p.Path != "" {
						p.GuessedLang = DetectLangFromPath(p.Path)
					}
					p.MarkBodyStreamed()
					if !p.TitlePrinted && (!p.NeedsPath() || strings.TrimSpace(p.Path) != "") {
						p.PrintStreamTitle(w, theme)
						p.FlushOutputBuf(w, theme)
					}
				}
			case '}', ']':
				if p.IsContent && p.LineBuffer.Len() > 0 {
					p.EmitLine(w, theme)
				}
				if !p.TitlePrinted {
					if !p.NeedsPath() || strings.TrimSpace(p.Path) != "" {
						p.PrintStreamTitle(w, theme)
					}
					p.FlushOutputBuf(w, theme)
				}
				p.InValue = false
				p.IsContent = false
				p.IsPath = false
			case ',':
				if p.IsContent && p.LineBuffer.Len() > 0 {
					p.EmitLine(w, theme)
				}
				p.InValue = false
				p.IsContent = false
				p.IsPath = false
			}
			i++
		}
	}
}

func (p *JSONStreamParser) formatCurrentTitle(theme style.UITheme) string {
	symbol := render.RenderToolSymbol(p.ActiveToolName, render.ToolStatusPending, theme)
	if p.ActiveToolName == "bash" {
		return render.FormatBashCommandLine(symbol, p.Path, theme)
	}
	return render.FormatToolTitle(symbol, p.ActiveToolName, p.Path, theme)
}

func (p *JSONStreamParser) PrintStreamTitle(w io.Writer, theme style.UITheme) {
	if p.TitlePrinted || (p.NeedsPath() && strings.TrimSpace(p.Path) == "") {
		return
	}
	p.TitlePrinted = true
	p.EnsureTrackingIndex()
	p.ToolTitlePrinted[p.ActiveToolIndex] = true
	p.ToolTitleLineNumbers[p.ActiveToolIndex] = GetNewlineCount(w)
	fmt.Fprintln(w, p.formatCurrentTitle(theme))
}

func (p *JSONStreamParser) UpdateStreamTitleWithPath(w io.Writer, theme style.UITheme) {
	p.EnsureTrackingIndex()
	if !p.TitlePrinted {
		if !p.NeedsPath() || strings.TrimSpace(p.Path) != "" {
			p.PrintStreamTitle(w, theme)
			p.FlushOutputBuf(w, theme)
		}
		return
	}
	if line := p.ToolTitleLineNumbers[p.ActiveToolIndex]; line >= 0 {
		_ = ReplaceTrackedStreamLine(w, line, p.formatCurrentTitle(theme))
	}
}

func (p *JSONStreamParser) EnsureTrackingIndex() {
	for len(p.ToolTitleLineNumbers) <= p.ActiveToolIndex {
		p.ToolTitleLineNumbers = append(p.ToolTitleLineNumbers, -1)
	}
	for len(p.ToolTitlePrinted) <= p.ActiveToolIndex {
		p.ToolTitlePrinted = append(p.ToolTitlePrinted, false)
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
				_, _ = buf.WriteString(strings.Join(lines, "\n"))
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
