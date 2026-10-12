package search

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

type findTool struct{}

// NewFindTool creates a new file finder tool.
func NewFindTool() domaintool.ToolExecutor {
	return &findTool{}
}

func (t *findTool) Name() string { return "find" }

func (t *findTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Find files by glob pattern")
}

func (t *findTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), nil)
}

func (t *findTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"find",
		domaintool.FormatToolDescription("find", "Find files by glob pattern. Returns up to 500 results (max 2000). Notices state the exact next offset and the total result count, so use offset to continue."),
		map[string]domaintool.SchemaProp{
			"pattern": domaintool.StringProp(domaintool.FormatParamDescription("find", "pattern", "Glob pattern to match files (e.g. *.py, **/*.json)")),
			"path":    domaintool.StringProp(domaintool.FormatParamDescription("find", "path", "Directory to search in (default: current directory)")),
			"limit":   domaintool.NumberProp(domaintool.FormatParamDescription("find", "limit", "Maximum results to return (default: 500)")),
			"offset":  domaintool.NumberProp(domaintool.FormatParamDescription("find", "offset", file.DefaultLimitTexts["find_param_offset"])),
		},
		"pattern",
	)
}

func (t *findTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Pattern string  `json:"pattern"`
		Glob    string  `json:"glob"`
		Path    string  `json:"path"`
		Dir     string  `json:"dir"`
		Limit   float64 `json:"limit"`
		Offset  float64 `json:"offset"`
	}

	if err := domaintool.ParseToolArguments(arguments, &args, func(p string) { args.Pattern = p }); err != nil {
		return "", err
	}

	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		pattern = strings.TrimSpace(args.Glob)
	}
	if pattern == "" {
		return "", fmt.Errorf("pattern is required for find (e.g. '*.py' or '**/*.json')")
	}

	searchPath := args.Path
	if searchPath == "" {
		searchPath = args.Dir
	}
	if searchPath == "" {
		searchPath = "."
	}

	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = limits.FindMaxResults()
	} else if limit > limits.FindMaxResults() {
		limit = limits.FindMaxResults()
	}

	offset := int(args.Offset)
	if offset < 1 {
		offset = 1
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	lowerPattern := strings.ToLower(pattern)
	hasSlash := strings.Contains(pattern, "/")

	// Matches are counted even past the cap so the notice can state the exact
	// total and the exact next offset; only collection stops.
	var matchedPaths []string
	totalMatches := 0
	limitReached := false

	if !info.IsDir() {
		rel, err := filepath.Rel(workspaceRoot, safePath)
		if err != nil {
			rel = safePath
		}
		rel = filepath.ToSlash(rel)
		totalMatches++
		if totalMatches >= offset && len(matchedPaths) < limit {
			matchedPaths = append(matchedPaths, rel)
		}
		if totalMatches >= offset+limit {
			limitReached = true
		}
	} else {
		err = filepath.WalkDir(safePath, func(path string, d fs.DirEntry, err error) error {
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

			rel, err := filepath.Rel(workspaceRoot, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)

			var matched bool
			if hasSlash {
				matched, _ = filepath.Match(lowerPattern, strings.ToLower(rel))
				if !matched {
					matched, _ = filepath.Match("*/"+lowerPattern, strings.ToLower(rel))
				}
			} else {
				matched, _ = filepath.Match(lowerPattern, strings.ToLower(name))
			}

			if matched {
				totalMatches++
				if totalMatches >= offset && len(matchedPaths) < limit {
					matchedPaths = append(matchedPaths, rel)
				}
				if totalMatches >= offset+limit {
					limitReached = true
				}
			}

			return nil
		})
		if err != nil && err != fs.SkipAll {
			return "", err
		}
	}

	if len(matchedPaths) == 0 {
		return fmt.Sprintf("No files found matching pattern '%s' in '%s'", pattern, searchPath), nil
	}

	sort.Strings(matchedPaths)

	// The byte budget applies to complete result lines, first hit wins.
	cut := file.TruncateHead(matchedPaths, len(matchedPaths), limits.MaxOutputBytes())
	result := cut.Content
	if limitReached || cut.Truncated {
		result += file.SearchLimitNotice("find", cut.ShownLines, totalMatches, cut.ShownLines+offset)
	}
	return result, nil
}
