package search

import (
	"path/filepath"
	"regexp"
	"strings"
)

type languageParser interface {
	Parse(string, []byte) goMetadata
}
type goSourceParser struct{}

func (goSourceParser) Parse(path string, data []byte) goMetadata { return parseGoMetadata(path, data) }

type scriptSourceParser struct{ python bool }

func parserFor(path string) languageParser {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return goSourceParser{}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return scriptSourceParser{}
	case ".py", ".pyi":
		return scriptSourceParser{python: true}
	}
	return nil
}

var pythonDeclaration = regexp.MustCompile(`^\s*(?:async\s+)?(def|class)\s+([A-Za-z_][A-Za-z_0-9]*)`)
var scriptDeclaration = regexp.MustCompile(`^\s*(?:(?:export|default|declare|async|abstract)\s+)*(function|class|interface|type|enum|const|let|var)\s+([A-Za-z_$][A-Za-z_0-9$]*)`)

// Lightweight lexical declarations complement the Go AST. Strings/comments are
// masked first; these results are locations, not type or reference resolution.
func (p scriptSourceParser) Parse(_ string, data []byte) goMetadata {
	masked := maskScript(string(data), p.python)
	lines := strings.Split(masked, "\n")
	result := goMetadata{}
	for i, line := range lines {
		match := scriptDeclaration.FindStringSubmatch(line)
		if p.python {
			match = pythonDeclaration.FindStringSubmatch(line)
		}
		if len(match) == 0 {
			continue
		}
		if len(result.symbols) == 256 {
			result.partial = true
			break
		}
		end := min(len(lines), i+40)
		if p.python {
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			end = i + 1
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) != "" && len(lines[j])-len(strings.TrimLeft(lines[j], " \t")) <= indent {
					break
				}
				end = j + 1
			}
		}
		result.symbols = append(result.symbols, codeSymbol{name: match[2], kind: match[1], line: i + 1, endLine: end})
	}
	return result
}

func maskScript(source string, python bool) string {
	data := []byte(source)
	quote := byte(0)
	triple, block, lineComment, escaped := false, false, false, false
	for i := 0; i < len(data); i++ {
		ch := source[i]
		if ch == '\n' {
			lineComment = false
			if quote != 0 && !triple && quote != '`' && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if lineComment {
			data[i] = ' '
			continue
		}
		if block {
			data[i] = ' '
			if ch == '*' && i+1 < len(data) && source[i+1] == '/' {
				data[i+1] = ' '
				i++
				block = false
			}
			continue
		}
		if quote != 0 {
			data[i] = ' '
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				if !triple {
					quote = 0
				} else if i+2 < len(data) && source[i+1] == quote && source[i+2] == quote {
					data[i+1], data[i+2] = ' ', ' '
					i += 2
					quote = 0
					triple = false
				}
			}
			continue
		}
		if python && ch == '#' {
			data[i] = ' '
			lineComment = true
			continue
		}
		if !python && ch == '/' && i+1 < len(data) {
			if source[i+1] == '/' {
				data[i], data[i+1] = ' ', ' '
				i++
				lineComment = true
				continue
			}
			if source[i+1] == '*' {
				data[i], data[i+1] = ' ', ' '
				i++
				block = true
				continue
			}
		}
		if ch == '\'' || ch == '"' || (!python && ch == '`') {
			quote = ch
			data[i] = ' '
			if python && i+2 < len(data) && source[i+1] == ch && source[i+2] == ch {
				data[i+1], data[i+2] = ' ', ' '
				i += 2
				triple = true
			}
		}
	}
	return string(data)
}
