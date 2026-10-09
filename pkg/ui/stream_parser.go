package ui

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type jsonStreamParser struct {
	inString    bool
	inEscape    bool
	currentKey  string
	inValue     bool
	buf         strings.Builder
	isContent   bool
	isPath      bool
	pathPrinted bool

	// Fields for syntax highlighting streamed code
	path       string
	lineBuffer strings.Builder

	// Field for language auto-detection
	guessedLang string

	// Fields for path-in-title rendering
	activeToolName string
	titlePrinted   bool

	// Output buffer for lazy rendering when path is not yet known
	outputBuf strings.Builder

	needsLeadingNewline  bool
	toolTitleLineNumbers []int
	toolBodyStreamed     []bool
	activeToolIndex      int
	streamWrites         bool
}

func isToolTargetPathKey(key string, toolName string) bool {
	if toolName == "todo" {
		return false
	}
	switch key {
	case "path", "file_path", "filePath", "file", "target", "Target", "target_file", "targetFile", "TargetFile",
		"filename", "fileName", "file_name", "write_path", "writePath", "AbsolutePath", "absolute_path",
		"SearchPath", "searchPath", "DirectoryPath", "dirPath", "directory_path", "pattern", "query", "Query",
		"prompt", "Prompt", "name", "id", "task_id", "url", "URL", "uri", "URI":
		return true
	case "command", "CommandLine", "cmd", "script", "code", "input", "arguments", "args":
		return toolName == "bash" || toolName == "ls" || strings.Contains(toolName, "command") || strings.Contains(toolName, "exec") || strings.Contains(toolName, "run") || strings.Contains(toolName, "shell")
	}
	return false
}

func isToolContentKey(key string, toolName string) bool {
	if isToolTargetPathKey(key, toolName) {
		return false
	}
	lower := strings.ToLower(key)
	if toolName == "todo" {
		return lower == "task" || lower == "title" || lower == "description" || lower == "content"
	}
	if lower == "description" || lower == "summary" || lower == "explanation" || lower == "overwrite" || lower == "encoding" || lower == "background" {
		return false
	}
	if isWriteLikeTool(toolName) {
		return true
	}
	return strings.Contains(lower, "content") || lower == "code" || lower == "body" || lower == "text" || lower == "data" || lower == "payload" || lower == "raw" || lower == "yaml" || lower == "yml" || lower == "json" || lower == "markdown" || lower == "md"
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit" || p.activeToolName == "grep" || p.activeToolName == "find" || p.activeToolName == "bash" || p.activeToolName == "ls" || p.activeToolName == "list" || p.activeToolName == "create_subagent" || p.activeToolName == "spawn_subagent" || p.activeToolName == "load_skill" || p.activeToolName == "task_status" || p.activeToolName == "task_kill" || p.activeToolName == "list_subagents" || p.activeToolName == "audit_subagent" || strings.HasPrefix(p.activeToolName, "subagent__")
}

func detectLangFromContent(content string) string {
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

func detectLangFromPath(path string) string {
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

func (p *jsonStreamParser) emitLine(w io.Writer, theme UITheme) {
	line := p.lineBuffer.String()
	p.lineBuffer.Reset()

	if p.needsPath() && p.path == "" && !p.titlePrinted && !p.isPath {
		p.outputBuf.WriteString(line)
		p.outputBuf.WriteByte('\n')
		return
	}

	if !p.titlePrinted {
		p.printStreamTitle(w, theme)
		p.flushOutputBuf(w, theme)
	}

	if p.guessedLang == "" && p.path != "" {
		p.guessedLang = detectLangFromPath(p.path)
	}
	if p.guessedLang == "" || p.guessedLang == "plaintext" {
		if detected := detectLangFromContent(line); detected != "" {
			p.guessedLang = detected
		}
	}
	lang := p.guessedLang
	if lang == "" {
		lang = "plaintext"
	}
	_ = HighlightWithoutTrailingNewline(w, line, lang, theme.ChromaStyle)
	fmt.Fprint(w, "\n")
}

func (p *jsonStreamParser) flushOutputBuf(w io.Writer, theme UITheme) {
	if p.outputBuf.Len() == 0 {
		return
	}
	raw := p.outputBuf.String()
	p.outputBuf.Reset()

	if p.guessedLang == "" && p.path != "" {
		p.guessedLang = detectLangFromPath(p.path)
	}
	if p.guessedLang == "" || p.guessedLang == "plaintext" {
		if detected := detectLangFromContent(raw); detected != "" {
			p.guessedLang = detected
		}
	}
	lang := p.guessedLang
	if lang == "" {
		lang = "plaintext"
	}

	lines := strings.Split(raw, "\n")
	for idx, l := range lines {
		if idx == len(lines)-1 && l == "" {
			break
		}
		_ = HighlightWithoutTrailingNewline(w, l, lang, theme.ChromaStyle)
		fmt.Fprint(w, "\n")
	}
}

func (p *jsonStreamParser) emitContent(s string, w io.Writer, theme UITheme) {
	if !p.titlePrinted {
		p.printStreamTitle(w, theme)
		p.flushOutputBuf(w, theme)
	}

	fmt.Fprint(w, s)
}

func (p *jsonStreamParser) feed(chunk string, w io.Writer, theme UITheme) {
	for i := 0; i < len(chunk); i++ {
		char := chunk[i]

		if p.inString {
			if p.inEscape {
				p.inEscape = false
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

				if p.inValue {
					if p.isContent {
						if p.activeToolName == "todo" {
							p.emitContent(unescaped, w, theme)
						} else {
							if unescaped == "\n" {
								p.emitLine(w, theme)
							} else {
								p.lineBuffer.WriteString(unescaped)
							}
						}
					} else if p.isPath {
						p.path += unescaped
					}
				} else {
					p.buf.WriteString(unescaped)
				}
			} else if char == '\\' {
				p.inEscape = true
			} else if char == '"' {
				p.inString = false
				strVal := p.buf.String()
				p.buf.Reset()

				if !p.inValue {
					p.currentKey = strVal
				} else {
					if p.isPath {
						if !p.titlePrinted {
							p.pathPrinted = true
							if p.guessedLang == "" && p.path != "" {
								p.guessedLang = detectLangFromPath(p.path)
							}
							p.printStreamTitle(w, theme)
							p.flushOutputBuf(w, theme)
						} else if !p.pathPrinted {
							p.pathPrinted = true
							if p.guessedLang == "" && p.path != "" {
								p.guessedLang = detectLangFromPath(p.path)
							}
							p.updateStreamTitleWithPath(w, theme)
						}
					}
					if p.activeToolName == "todo" && p.isContent {
						fmt.Fprintln(w)
					}
					if p.isContent {
						if p.lineBuffer.Len() > 0 {
							p.emitLine(w, theme)
						}
					}
					p.inValue = false
					p.isContent = false
					p.isPath = false
				}
			} else {
				if p.inValue {
					charStr := string(char)
					if p.isContent {
						if p.activeToolName == "todo" {
							p.emitContent(charStr, w, theme)
						} else {
							if char == '\n' {
								p.emitLine(w, theme)
							} else {
								p.lineBuffer.WriteString(charStr)
							}
						}
					} else if p.isPath {
						p.path += charStr
					}
				} else {
					p.buf.WriteByte(char)
				}
			}
		} else {
			if char == '"' {
				p.inString = true
			} else if char == '{' {
				p.inValue = false
			} else if char == ':' {
				p.inValue = true
				if isToolTargetPathKey(p.currentKey, p.activeToolName) {
					if p.path == "" {
						p.isPath = true
					}
				} else if p.activeToolName != "edit" && isToolContentKey(p.currentKey, p.activeToolName) {
					if p.streamWrites {
						p.isContent = true
						if p.activeToolName == "todo" && (p.currentKey == "task" || p.currentKey == "title" || p.currentKey == "description" || p.currentKey == "content") {
							fmt.Fprint(w, "  • ")
						}
						if p.guessedLang == "" && p.path != "" {
							p.guessedLang = detectLangFromPath(p.path)
						}
						p.markBodyStreamed()
						if !p.titlePrinted {
							if !p.needsPath() || p.path != "" {
								p.printStreamTitle(w, theme)
								p.flushOutputBuf(w, theme)
							}
						}
					}
				}
			} else if char == '}' || char == ']' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					p.emitLine(w, theme)
				}
				if !p.titlePrinted {
					p.printStreamTitle(w, theme)
					p.flushOutputBuf(w, theme)
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
			} else if char == ',' {
				if p.isContent && p.lineBuffer.Len() > 0 {
					p.emitLine(w, theme)
				}
				p.inValue = false
				p.isContent = false
				p.isPath = false
			}
		}
	}
}

func (p *jsonStreamParser) printStreamTitle(w io.Writer, theme UITheme) {
	if p.titlePrinted {
		return
	}
	if p.activeToolName == "bash" && strings.TrimSpace(p.path) == "" {
		return
	}
	p.titlePrinted = true
	p.ensureTrackingIndex()

	if p.activeToolName == "bash" {
		p.toolTitleLineNumbers[p.activeToolIndex] = getNewlineCount(w)
		symbol := renderToolSymbol(p.activeToolName, toolStatusPending, theme)
		fmt.Fprintln(w, FormatBashCommandLine(symbol, p.path, theme))
		return
	}

	p.toolTitleLineNumbers[p.activeToolIndex] = getNewlineCount(w)
	symbol := renderToolSymbol(p.activeToolName, toolStatusPending, theme)
	fmt.Fprintln(w, FormatToolTitle(symbol, p.activeToolName, p.path, theme))
}

func (p *jsonStreamParser) updateStreamTitleWithPath(w io.Writer, theme UITheme) {
	p.ensureTrackingIndex()
	line := p.toolTitleLineNumbers[p.activeToolIndex]
	symbol := renderToolSymbol(p.activeToolName, toolStatusPending, theme)
	if p.activeToolName == "bash" {
		replaceTrackedStreamLine(w, line, FormatBashCommandLine(symbol, p.path, theme))
		return
	}
	replaceTrackedStreamLine(w, line, FormatToolTitle(symbol, p.activeToolName, p.path, theme))
}

func (p *jsonStreamParser) ensureTrackingIndex() {
	for len(p.toolTitleLineNumbers) <= p.activeToolIndex {
		p.toolTitleLineNumbers = append(p.toolTitleLineNumbers, -1)
	}
	for len(p.toolBodyStreamed) <= p.activeToolIndex {
		p.toolBodyStreamed = append(p.toolBodyStreamed, false)
	}
}

func (p *jsonStreamParser) markBodyStreamed() {
	p.ensureTrackingIndex()
	p.toolBodyStreamed[p.activeToolIndex] = true
}

func getNewlineCount(w io.Writer) int {
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

func replaceTrackedStreamLine(w io.Writer, line int, content string) bool {
	if line < 0 {
		return false
	}
	currentLine := getNewlineCount(w)
	if currentLine < line {
		return false
	}
	if writer := findPromptPreservingWriter(w); writer != nil {
		return writer.ReplaceScrollLineBack(currentLine-line, content)
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
