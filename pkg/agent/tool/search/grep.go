package search

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

func defaultGrepLimit() int   { return limits.GrepDefaultLimit() }
func maxGrepLimit() int       { return limits.GrepMaxLimit() }
func maxGrepLineLength() int  { return limits.GrepMaxLineLength() }
func maxGrepOutputBytes() int { return limits.GrepMaxOutputBytes() }
func maxGrepFileSize() int    { return limits.GrepMaxFileBytes() }

type grepTool struct{}

func NewGrepTool() domaintool.ToolExecutor {
	return &grepTool{}
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Search file contents for patterns or regular expressions")
}

func (t *grepTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), nil)
}

func (t *grepTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"grep",
		domaintool.FormatToolDescription("grep", "Search file contents for regular expressions or literal strings across the workspace (like grep -rn). Returns matching file paths, line numbers, and lines. If pattern is empty and glob is provided, lists matching file paths. Automatically respects .gitignore and ignores dependency/build directories. Returns up to 100 matches (max 500), caps each matched line at 500 characters and the total output at 64KB. Notices state the exact next offset and the total matching lines, so use offset to continue."),
		map[string]domaintool.SchemaProp{
			"pattern":     domaintool.StringProp(domaintool.FormatParamDescription("grep", "pattern", "Regular expression pattern to search for (or literal string). If empty and glob is provided, matches file names.")),
			"path":        domaintool.StringProp(domaintool.FormatParamDescription("grep", "path", "Directory or file to search in (default: current directory).")),
			"glob":        domaintool.StringProp(domaintool.FormatParamDescription("grep", "glob", "Optional file glob pattern to filter files by name (e.g. '*.go', '*.py').")),
			"ignore_case": domaintool.BoolProp(domaintool.FormatParamDescription("grep", "ignore_case", "Case-insensitive search (default: false).")),
			"literal":     domaintool.BoolProp(domaintool.FormatParamDescription("grep", "literal", "Treat pattern as a literal string instead of a regular expression (default: false).")),
			"context":     domaintool.NumberProp(domaintool.FormatParamDescription("grep", "context", "Number of lines of leading and trailing context to show around each match (default: 0).")),
			"limit":       domaintool.NumberProp(domaintool.FormatParamDescription("grep", "limit", "Maximum number of matching lines to return (default: 100, max: 500).")),
			"offset":      domaintool.NumberProp(domaintool.FormatParamDescription("grep", "offset", file.DefaultLimitTexts["grep_param_offset"])),
		},
	)
}

func buildLineMatcher(pattern string, literal, ignoreCase bool) (func(string) bool, error) {
	if pattern == "" {
		return nil, nil
	}
	if literal {
		if ignoreCase {
			lowerPattern := strings.ToLower(pattern)
			return func(line string) bool {
				return strings.Contains(strings.ToLower(line), lowerPattern)
			}, nil
		}
		return func(line string) bool {
			return strings.Contains(line, pattern)
		}, nil
	}
	regexPattern := pattern
	if ignoreCase && !strings.HasPrefix(regexPattern, "(?i)") {
		regexPattern = "(?i)" + regexPattern
	}
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regular expression '%s': %w", pattern, err)
	}
	return re.MatchString, nil
}

func sortedIntKeys(m map[int]bool) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func (t *grepTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Pattern         string  `json:"pattern"`
		Query           string  `json:"query"`
		Q               string  `json:"q"`
		Path            string  `json:"path"`
		SearchPath      string  `json:"search_path"`
		Glob            string  `json:"glob"`
		IgnoreCase      bool    `json:"ignore_case"`
		IgnoreCaseCamel bool    `json:"ignoreCase"`
		Literal         bool    `json:"literal"`
		Context         float64 `json:"context"`
		Limit           float64 `json:"limit"`
		Offset          float64 `json:"offset"`
	}

	if err := domaintool.ParseToolArguments(arguments, &args, func(p string) { args.Pattern = p }); err != nil {
		return "", err
	}

	pattern := domaintool.FirstNonEmpty(args.Pattern, args.Query, args.Q)
	globPattern := strings.TrimSpace(args.Glob)
	if pattern == "" && globPattern == "" {
		return "", fmt.Errorf("pattern is required for grep (or provide a glob pattern to list matching files, e.g. glob='*.py')")
	}

	searchPath := domaintool.FirstNonEmpty(args.Path, args.SearchPath, ".")
	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = defaultGrepLimit()
	} else if limit > maxGrepLimit() {
		limit = maxGrepLimit()
	}

	offset := int(args.Offset)
	if offset < 1 {
		offset = 1
	}

	contextLines := int(args.Context)
	if contextLines < 0 {
		contextLines = 0
	} else if contextLines > 5 {
		contextLines = 5
	}

	matchFunc, err := buildLineMatcher(pattern, args.Literal, args.IgnoreCase || args.IgnoreCaseCamel)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	var out strings.Builder
	shownMatches := 0
	totalMatches := 0
	limitReached := false
	outputSizeReached := false
	maxOutBytes := maxGrepOutputBytes()
	maxLineLen := maxGrepLineLength()
	maxFileBytes := int64(maxGrepFileSize())

	appendOutputLine := func(lineStr string) bool {
		if out.Len()+len(lineStr) > maxOutBytes {
			outputSizeReached = true
			return false
		}
		out.WriteString(lineStr)
		return true
	}

	processFile := func(filePath string) error {
		if selectErr := ctx.Context().Err(); selectErr != nil {
			return selectErr
		}

		fInfo, err := os.Stat(filePath)
		if err != nil || fInfo.IsDir() || fInfo.Size() == 0 || fInfo.Size() > maxFileBytes {
			return nil
		}

		relPath := RelSlashPath(workspaceRoot, filePath)
		if pattern == "" {
			totalMatches++
			if totalMatches < offset {
				return nil
			}
			if shownMatches >= limit {
				limitReached = true
				return fs.SkipAll
			}
			out.WriteString(relPath + "\n")
			shownMatches++
			return nil
		}

		data, err := os.ReadFile(filePath)
		if err != nil || IsBinary(data) {
			return nil
		}

		fileLines := strings.Split(SanitizeUTF8(data), "\n")
		numLines := len(fileLines)

		matchedIndices := make(map[int]bool)
		for idx, line := range fileLines {
			if matchFunc(line) {
				totalMatches++
				if totalMatches >= offset && totalMatches < offset+limit {
					matchedIndices[idx] = true
					shownMatches++
				} else if totalMatches >= offset+limit {
					limitReached = true
				}
			}
		}

		if len(matchedIndices) == 0 {
			return nil
		}

		if contextLines == 0 {
			for _, idx := range sortedIntKeys(matchedIndices) {
				lineContent := truncateGrepLine(fileLines[idx], maxLineLen)
				if !appendOutputLine(fmt.Sprintf("%s:%d: %s\n", relPath, idx+1, lineContent)) {
					return fs.SkipAll
				}
			}
		} else {
			linesToShow := make(map[int]bool, len(matchedIndices)*(2*contextLines+1))
			for mIdx := range matchedIndices {
				start := mIdx - contextLines
				if start < 0 {
					start = 0
				}
				end := mIdx + contextLines
				if end >= numLines {
					end = numLines - 1
				}
				for c := start; c <= end; c++ {
					linesToShow[c] = true
				}
			}

			prevLine := -1
			for _, idx := range sortedIntKeys(linesToShow) {
				if prevLine != -1 && idx > prevLine+1 {
					out.WriteString("--\n")
				}
				lineContent := truncateGrepLine(fileLines[idx], maxLineLen)
				sep := "-"
				if matchedIndices[idx] {
					sep = ":"
				}
				if !appendOutputLine(fmt.Sprintf("%s:%d%s %s\n", relPath, idx+1, sep, lineContent)) {
					return fs.SkipAll
				}
				prevLine = idx
			}
		}

		if limitReached {
			return fs.SkipAll
		}
		return nil
	}

	if !info.IsDir() {
		_ = processFile(safePath)
	} else {
		walkErr := filepath.WalkDir(safePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if skip, skipErr := ShouldSkipWalkEntry(path, d, workspaceRoot, gitIgnorePatterns); skip {
				return skipErr
			}
			if globPattern != "" {
				rel := RelSlashPath(workspaceRoot, path)
				relToSearch := RelSlashPath(safePath, path)
				if !MatchGlob(globPattern, rel, d.Name()) && (relToSearch == rel || !MatchGlob(globPattern, relToSearch, d.Name())) {
					return nil
				}
			}
			return processFile(path)
		})
		if walkErr != nil && walkErr != fs.SkipAll {
			return "", walkErr
		}
	}

	result := out.String()
	if strings.TrimSpace(result) == "" {
		if pattern == "" && globPattern != "" {
			return fmt.Sprintf("No files found matching glob: %s", globPattern), nil
		}
		return fmt.Sprintf("No matches found for pattern: %s", pattern), nil
	}

	if limitReached || outputSizeReached {
		next := offset + shownMatches
		if outputSizeReached && !limitReached {
			result += file.GrepOutputNotice(maxOutBytes/1024, shownMatches, totalMatches, next)
		} else {
			result += file.GrepLimitNotice(shownMatches, totalMatches, next)
		}
	}

	return result, nil
}

func truncateGrepLine(s string, maxRunes int) string {
	return file.TruncateLine(s, maxRunes)
}