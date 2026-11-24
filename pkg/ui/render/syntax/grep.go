package syntax

import (
	"regexp"
	"strconv"
	"strings"

	"loop/pkg/ui/style"
)

// Native grep uses path:line: source for matches and path:line- source for
// context. Parse the prefix separately so filenames are not treated as code.
var grepSourceLine = regexp.MustCompile(`^(.+?):([0-9]+)([:-]) (.*)$`)

// Bash grep/rg/git-grep often omit the space after the delimiter (path:line:code).
var bashGrepSourceLine = regexp.MustCompile(`^([^:\s][^:]*):([0-9]+)([:-])(.*)$`)

func HighlightGrepBody(body string, theme style.UITheme) string {
	var result strings.Builder
	var previousPath, prefix, language string
	previousLine := 0
	for _, raw := range strings.SplitAfter(body, "\n") {
		line := strings.TrimSuffix(raw, "\n")
		match := grepSourceLine.FindStringSubmatchIndex(line)
		if match == nil {
			result.WriteString(raw)
			previousPath, prefix = "", ""
			continue
		}
		path := line[match[2]:match[3]]
		number, err := strconv.Atoi(line[match[4]:match[5]])
		if err != nil || number < 1 {
			result.WriteString(raw)
			previousPath, prefix = "", ""
			continue
		}
		if path != previousPath {
			language = ResolveSyntaxLanguage(path, "")
		}
		if path != previousPath || number != previousLine+1 {
			prefix = ""
		}
		code := line[match[8]:match[9]]
		colored := highlightSyntaxFragment(prefix, code, language, theme.ChromaStyle)
		if colored != code {
			result.WriteString(style.NewStyle().Foreground(theme.Text).Render(line[:match[8]]))
			result.WriteString(colored)
			result.WriteString(raw[len(line):])
		} else {
			result.WriteString(raw)
		}
		prefix = syntaxContextTail(prefix + code + "\n")
		previousPath, previousLine = path, number
	}
	return result.String()
}

// LooksLikeBashGrepOutput reports whether body contains path:line:code lines
// whose file paths map to a recognized syntax language.
func LooksLikeBashGrepOutput(body string) bool {
	checked := 0
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		checked++
		if _, _, _, ok := parseBashGrepLine(line); ok {
			return true
		}
		if checked >= 8 {
			break
		}
	}
	return false
}

func parseBashGrepLine(line string) (path string, lineNo int, codeStart int, ok bool) {
	match := bashGrepSourceLine.FindStringSubmatchIndex(line)
	if match == nil {
		return "", 0, 0, false
	}
	path = line[match[2]:match[3]]
	// Avoid matching timestamps or URLs like http://host:8080/path.
	if strings.Contains(path, "://") {
		return "", 0, 0, false
	}
	number, err := strconv.Atoi(line[match[4]:match[5]])
	if err != nil || number < 1 {
		return "", 0, 0, false
	}
	lang := ResolveSyntaxLanguage(path, "")
	if lang == "" || lang == "plaintext" {
		return "", 0, 0, false
	}
	return path, number, match[8], true
}

// HighlightBashGrepBody highlights grep/rg/git-grep output from bash commands,
// styling file:line: prefixes separately from syntax-highlighted code content.
func HighlightBashGrepBody(body string, theme style.UITheme) string {
	if style.ChromaFormatterName() == "" {
		return body
	}
	var result strings.Builder
	result.Grow(len(body))
	var previousPath, prefix, language string
	previousLine := 0
	pathStyle := style.NewStyle().Foreground(theme.Secondary)
	metaStyle := style.NewStyle().Foreground(theme.TextMuted)
	for _, raw := range strings.SplitAfter(body, "\n") {
		if raw == "" {
			continue
		}
		line := strings.TrimSuffix(raw, "\n")
		cr := ""
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
			cr = "\r"
		}
		nl := raw[len(line)+len(cr):]
		path, number, codeStart, ok := parseBashGrepLine(line)
		if !ok {
			result.WriteString(raw)
			previousPath, prefix = "", ""
			continue
		}
		if path != previousPath {
			language = ResolveSyntaxLanguage(path, "")
		}
		if path != previousPath || number != previousLine+1 {
			prefix = ""
		}
		code := line[codeStart:]
		coloredCode := highlightSyntaxFragment(prefix, code, language, theme.ChromaStyle)
		coloredLine := pathStyle.Render(path) + metaStyle.Render(line[len(path):codeStart]) + coloredCode
		if style.StripAnsi(coloredLine) == line {
			result.WriteString(coloredLine)
			result.WriteString(cr)
			result.WriteString(nl)
		} else {
			result.WriteString(raw)
		}
		prefix = syntaxContextTail(prefix + code + "\n")
		previousPath, previousLine = path, number
	}
	return result.String()
}
