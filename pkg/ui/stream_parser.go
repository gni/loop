package ui

import (
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
	switch key {
	case "path", "file_path", "filePath", "file", "target", "Target", "target_file", "targetFile",
		"filename", "fileName", "AbsolutePath", "TargetFile", "SearchPath", "searchPath",
		"DirectoryPath", "dirPath", "directory_path", "pattern", "query", "Query",
		"prompt", "Prompt", "name", "id", "task_id":
		return true
	case "command", "CommandLine", "cmd":
		return toolName == "bash" || toolName == "ls" || strings.Contains(toolName, "command") || strings.Contains(toolName, "exec") || strings.Contains(toolName, "run")
	}
	return false
}

func (p *jsonStreamParser) needsPath() bool {
	return p.activeToolName == "read" || p.activeToolName == "write" || p.activeToolName == "edit" || p.activeToolName == "grep" || p.activeToolName == "find" || p.activeToolName == "bash" || p.activeToolName == "ls" || p.activeToolName == "list" || p.activeToolName == "spawn_subagent" || p.activeToolName == "load_skill" || p.activeToolName == "task_status" || p.activeToolName == "task_kill" || strings.HasPrefix(p.activeToolName, "subagent__")
}

func (p *jsonStreamParser) emitLine(w io.Writer, theme UITheme) {
	line := p.lineBuffer.String()
	p.lineBuffer.Reset()

	if p.needsPath() && p.path == "" && !p.titlePrinted && !p.isPath {
		p.outputBuf.WriteString(line)
		p.outputBuf.WriteByte('\n')
		return
	}

	if p.guessedLang == "" && p.path != "" {
		ext := filepath.Ext(p.path)
		if len(ext) > 1 {
			p.guessedLang = ext[1:]
		} else {
			p.guessedLang = "plaintext"
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
		ext := filepath.Ext(p.path)
		if len(ext) > 1 {
			p.guessedLang = ext[1:]
		} else {
			p.guessedLang = "plaintext"
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
						if unescaped == "\n" {
							p.emitLine(w, theme)
						} else {
							p.lineBuffer.WriteString(unescaped)
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
								ext := filepath.Ext(p.path)
								if len(ext) > 1 {
									p.guessedLang = ext[1:]
								} else {
									p.guessedLang = "plaintext"
								}
							}
							p.printStreamTitle(w, theme)
							p.flushOutputBuf(w, theme)
						} else if !p.pathPrinted {
							p.pathPrinted = true
							p.updateStreamTitleWithPath(w, theme)
						}
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
						if char == '\n' {
							p.emitLine(w, theme)
						} else {
							p.lineBuffer.WriteString(charStr)
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
			} else if char == ':' {
				p.inValue = true
				if isToolTargetPathKey(p.currentKey, p.activeToolName) {
					if p.path == "" {
						p.isPath = true
					}
				} else if p.activeToolName != "edit" && (p.currentKey == "write_content" || p.currentKey == "content" || strings.Contains(p.currentKey, "Content") || p.currentKey == "code" || p.currentKey == "text" || p.currentKey == "body") {
					if p.streamWrites {
						p.isContent = true
						p.guessedLang = ""
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
		unwrapper, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			break
		}
		w = unwrapper.Unwrap()
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
	writer := findPromptPreservingWriter(w)
	if writer == nil {
		return false
	}
	return writer.ReplaceScrollLineBack(currentLine-line, content)
}
