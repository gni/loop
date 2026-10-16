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

	ignoreCase := args.IgnoreCase || args.IgnoreCaseCamel
	literal := args.Literal

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

	var matchFunc func(line string) bool
	if pattern != "" {
		if literal {
			if ignoreCase {
				lowerPattern := strings.ToLower(pattern)
				matchFunc = func(line string) bool {
					return strings.Contains(strings.ToLower(line), lowerPattern)
				}
			} else {
				matchFunc = func(line string) bool {
					return strings.Contains(line, pattern)
				}
			}
		} else {
			regexPattern := pattern
			if ignoreCase && !strings.HasPrefix(regexPattern, "(?i)") {
				regexPattern = "(?i)" + regexPattern
			}
			re, err := regexp.Compile(regexPattern)
			if err != nil {
				return "", fmt.Errorf("invalid regular expression '%s': %w", pattern, err)
			}
			matchFunc = re.MatchString
		}
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	if globPattern != "" {
		globPattern = strings.ToLower(globPattern)
	}

	var out strings.Builder
	shownMatches := 0
	totalMatches := 0
	limitReached := false
	outputSizeReached := false

	processFile := func(filePath string) error {
		if selectErr := ctx.Context().Err(); selectErr != nil {
			return selectErr
		}

		fInfo, err := os.Stat(filePath)
		if err != nil || fInfo.IsDir() || fInfo.Size() == 0 || fInfo.Size() > int64(maxGrepFileSize()) {
			return nil
		}

		if pattern == "" {
			relPath, err := filepath.Rel(workspaceRoot, filePath)
			if err != nil {
				relPath = filePath
			}
			relPath = filepath.ToSlash(relPath)
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

		contentStr := SanitizeUTF8(data)
		fileLines := strings.Split(contentStr, "\n")
		numLines := len(fileLines)

		relPath, err := filepath.Rel(workspaceRoot, filePath)
		if err != nil {
			relPath = filePath
		}
		relPath = filepath.ToSlash(relPath)

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
			lineNums := make([]int, 0, len(matchedIndices))
			for idx := range matchedIndices {
				lineNums = append(lineNums, idx)
			}
			sort.Ints(lineNums)
			for _, idx := range lineNums {
				lineContent := truncateGrepLine(fileLines[idx], maxGrepLineLength())
				lineStr := fmt.Sprintf("%s:%d: %s\n", relPath, idx+1, lineContent)
				if out.Len()+len(lineStr) > maxGrepOutputBytes() {
					outputSizeReached = true
					return fs.SkipAll
				}
				out.WriteString(lineStr)
			}
		} else {
			linesToShow := make(map[int]bool)
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

			sortedLines := make([]int, 0, len(linesToShow))
			for idx := range linesToShow {
				sortedLines = append(sortedLines, idx)
			}
			sort.Ints(sortedLines)

			prevLine := -1
			for _, idx := range sortedLines {
				if prevLine != -1 && idx > prevLine+1 {
					out.WriteString("--\n")
				}
				lineContent := truncateGrepLine(fileLines[idx], maxGrepLineLength())
				sep := "-"
				if matchedIndices[idx] {
					sep = ":"
				}
				lineStr := fmt.Sprintf("%s:%d%s %s\n", relPath, idx+1, sep, lineContent)
				if out.Len()+len(lineStr) > maxGrepOutputBytes() {
					outputSizeReached = true
					return fs.SkipAll
				}
				out.WriteString(lineStr)
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

			name := d.Name()
			if d.IsDir() {
				if IsIgnoredDirName(name) || HasIgnoredComponentIn(path, workspaceRoot) || IsIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
					return filepath.SkipDir
				}
				return nil
			}

			if IsIgnoredFileName(name) || IsIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
				return nil
			}

			if globPattern != "" {
				matched, matchErr := filepath.Match(globPattern, strings.ToLower(name))
				if matchErr == nil && !matched {
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
			result += file.GrepOutputNotice(maxGrepOutputBytes()/1024, shownMatches, totalMatches, next)
		} else {
			result += file.GrepLimitNotice(shownMatches, totalMatches, next)
		}
	}

	return result, nil
}

func truncateGrepLine(s string, maxRunes int) string {
	return file.TruncateLine(s, maxRunes)
}