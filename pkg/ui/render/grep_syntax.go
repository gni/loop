package render

import (
	"regexp"
	"strconv"
	"strings"

	"loop/pkg/ui/style"
)

// Native grep uses path:line: source for matches and path:line- source for
// context. Parse the prefix separately so filenames are not treated as code.
var grepSourceLine = regexp.MustCompile(`^(.+?):([0-9]+)([:-]) (.*)$`)

func highlightGrepBody(body string, theme style.UITheme) string {
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
