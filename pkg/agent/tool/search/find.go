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

	pattern := domaintool.FirstNonEmpty(strings.TrimSpace(args.Pattern), strings.TrimSpace(args.Glob))
	if pattern == "" {
		return "", fmt.Errorf("pattern is required for find (e.g. '*.py' or '**/*.json')")
	}

	searchPath := domaintool.FirstNonEmpty(args.Path, args.Dir, ".")
	safePath, err := ctx.SafePath(searchPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", searchPath, err)
	}

	limit := int(args.Limit)
	if limit <= 0 || limit > limits.FindMaxResults() {
		limit = limits.FindMaxResults()
	}

	offset := int(args.Offset)
	if offset < 1 {
		offset = 1
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	var matchedPaths []string
	totalMatches := 0
	limitReached := false

	recordMatch := func(rel string) {
		totalMatches++
		if totalMatches >= offset && len(matchedPaths) < limit {
			matchedPaths = append(matchedPaths, rel)
		}
		if totalMatches >= offset+limit {
			limitReached = true
		}
	}

	if !info.IsDir() {
		rel := RelSlashPath(workspaceRoot, safePath)
		if MatchGlob(pattern, rel, filepath.Base(safePath)) {
			recordMatch(rel)
		}
	} else {
		err = filepath.WalkDir(safePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if skip, skipErr := ShouldSkipWalkEntry(path, d, workspaceRoot, gitIgnorePatterns); skip {
				return skipErr
			}

			rel := RelSlashPath(workspaceRoot, path)
			relToSearch := RelSlashPath(safePath, path)
			if MatchGlob(pattern, rel, d.Name()) || (relToSearch != rel && MatchGlob(pattern, relToSearch, d.Name())) {
				recordMatch(rel)
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

	cut := file.TruncateHead(matchedPaths, len(matchedPaths), limits.MaxOutputBytes())
	result := cut.Content
	if limitReached || cut.Truncated {
		result += file.SearchLimitNotice("find", cut.ShownLines, totalMatches, cut.ShownLines+offset)
	}
	return result, nil
}
