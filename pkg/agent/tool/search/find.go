package search

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type findTool struct{}

// NewFindTool creates a new file finder tool.
func NewFindTool() domaintool.ToolExecutor {
	return &findTool{}
}

func (t *findTool) Name() string { return "find" }

func (t *findTool) PromptSnippet() string {
	return "Find files by glob pattern"
}

func (t *findTool) PromptGuidelines() []string {
	return []string{
		"Use 'find' to locate files by glob pattern instead of running find in bash.",
	}
}

var findToolDef = domaintool.NewFunctionTool(
	"find",
	"Find files by glob pattern",
	map[string]domaintool.SchemaProp{
		"pattern": domaintool.StringProp("Glob pattern to match files (e.g. *.py, **/*.json)"),
		"path":    domaintool.StringProp("Directory to search in (default: current directory)"),
		"limit":   domaintool.NumberProp("Maximum results to return (default: 500)"),
	},
	"pattern",
)

func (t *findTool) Definition() domaintool.Tool {
	return findToolDef
}

func (t *findTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Pattern string  `json:"pattern"`
		Glob    string  `json:"glob"`
		Path    string  `json:"path"`
		Dir     string  `json:"dir"`
		Limit   float64 `json:"limit"`
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
		limit = 500
	} else if limit > 1000 {
		limit = 1000
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	lowerPattern := strings.ToLower(pattern)
	hasSlash := strings.Contains(pattern, "/")

	var matchedPaths []string
	limitReached := false

	if !info.IsDir() {
		rel, err := filepath.Rel(workspaceRoot, safePath)
		if err != nil {
			rel = safePath
		}
		rel = filepath.ToSlash(rel)
		matchedPaths = append(matchedPaths, rel)
	} else {
		err = filepath.WalkDir(safePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			name := d.Name()
			if d.IsDir() {
				if IsIgnoredDirName(name) || HasIgnoredComponent(path) || IsIgnoredByGit(path, workspaceRoot, gitIgnorePatterns) {
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
				matchedPaths = append(matchedPaths, rel)
				if len(matchedPaths) >= limit {
					limitReached = true
					return fs.SkipAll
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
	result := strings.Join(matchedPaths, "\n")
	if limitReached {
		result += fmt.Sprintf("\n\n[match limit reached: showing first %d matches. Use a more specific pattern to narrow down]", limit)
	}
	return result, nil
}
