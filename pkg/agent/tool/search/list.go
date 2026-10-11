package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

type listTool struct{}

// NewListTool creates a new directory listing tool.
func NewListTool() domaintool.ToolExecutor {
	return &listTool{}
}

func (t *listTool) Name() string { return "list" }

func (t *listTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "List directory contents")
}

func (t *listTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'list' to inspect directory trees; never call 'read' on a directory path.",
		"If a listing returns nothing relevant, stop searching and answer from internal knowledge instead of re-listing.",
	})
}

func (t *listTool) Definition() domaintool.Tool {
	return domaintool.Tool{
		Type: "function",
		Function: domaintool.FunctionDefinition{
			Name:        "list",
			Description: domaintool.FormatToolDescription("list", "List directory contents. Lists up to 150 entries (max 1000) at up to depth 3 (max 8). Notices state the exact next offset and the total entry count, so use offset to continue."),
			Parameters: domaintool.JSONSchema{
				Type: "object",
				Properties: map[string]domaintool.SchemaProp{
					"path": {
						Type:        "string",
						Description: domaintool.FormatParamDescription("list", "path", "Directory to list (default: current directory)"),
					},
					"depth": {
						Type:        "number",
						Description: domaintool.FormatParamDescription("list", "depth", "Depth to list (1 for flat, 2+ for recursive, default: 1)"),
					},
					"offset": {
						Type:        "number",
						Description: domaintool.FormatParamDescription("list", "offset", file.DefaultLimitTexts["list_param_offset"]),
					},
				},
			},
		},
	}
}

func (t *listTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path    string  `json:"path"`
		Dir     string  `json:"dir"`
		DirPath string  `json:"dir_path"`
		Depth   float64 `json:"depth"`
		Offset  float64 `json:"offset"`
	}

	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			args.Path = unquoted
		}
	} else if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
			args.Path = trimmed
		} else if trimmed != "" && trimmed != "{}" {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}

	targetPath := args.Path
	if targetPath == "" {
		if args.Dir != "" {
			targetPath = args.Dir
		} else if args.DirPath != "" {
			targetPath = args.DirPath
		}
	}
	if targetPath == "" {
		targetPath = "."
	}

	depth := int(args.Depth)
	if depth <= 0 {
		depth = limits.ListMaxDepth() // documented default, itself clamped
	} else if depth > limits.ListMaxDepth() {
		depth = limits.ListMaxDepth()
	}

	offset := int(args.Offset)
	if offset < 1 {
		offset = 1
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		// If path attempts to escape workspace root (e.g. '..' or parent directory),
		// gracefully list workspace root instead of failing with an error.
		safePath = ctx.GetWorkspaceRoot()
		tree, treeErr := ListDirectoryTree(safePath, safePath, depth, limits.ListMaxEntries())
		if treeErr != nil {
			return "", err
		}
		return fmt.Sprintf("[Notice: '%s' is outside workspace root. Showing workspace root ('%s') instead:]\n\n%s", targetPath, safePath, tree), nil
	}

	info, err := os.Stat(safePath)
	if err != nil {
		return "", fmt.Errorf("cannot access path '%s': %w", targetPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path '%s' is a file, not a directory. Use the 'read' tool to view its contents", targetPath)
	}

	return ListDirectoryTree(safePath, ctx.GetWorkspaceRoot(), depth, limits.ListMaxEntries())
}

// ListDirectoryTree traverses and renders a clean visual ASCII tree of the directory,
// automatically filtering out dependency, cache, and build directories. Entry counting
// continues past the cap so the notice can state the exact total entry count and the
// exact next offset.
func ListDirectoryTree(dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error) {
	return ListDirectoryTreeOffset(dirPath, workspaceRoot, maxDepth, maxEntries, 1)
}

func ListDirectoryTreeOffset(dirPath string, workspaceRoot string, maxDepth int, maxEntries int, offset int) (string, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if maxDepth > limits.ListMaxDepth() {
		maxDepth = limits.ListMaxDepth()
	}
	if maxEntries <= 0 {
		maxEntries = limits.ListMaxEntries()
	} else if maxEntries > limits.ListMaxEntries() {
		maxEntries = limits.ListMaxEntries()
	}
	if offset < 1 {
		offset = 1
	}

	gitIgnorePatterns := LoadGitIgnore(workspaceRoot)

	relBase, err := filepath.Rel(workspaceRoot, dirPath)
	var header string
	if err != nil || relBase == "." || relBase == "" {
		header = "./\n"
	} else {
		header = filepath.ToSlash(relBase) + "/\n"
	}

	var rendered []string
	rendered = append(rendered, strings.TrimRight(header, "\n"))

	shown := 0
	total := 0
	bytesUsed := 0
	limitReached := false
	bytesReached := false

	var walkTree func(currentDir string, prefix string, depth int)
	walkTree = func(currentDir string, prefix string, depth int) {
		// Traversal continues after a cap is hit so the notice can state the
		// exact total entry count and the exact next offset.
		if depth > maxDepth {
			return
		}

		entries, err := os.ReadDir(currentDir)
		if err != nil {
			return
		}

		var validEntries []os.DirEntry
		for _, entry := range entries {
			name := entry.Name()
			fullPath := filepath.Join(currentDir, name)

			if entry.IsDir() {
				if IsIgnoredDirName(name) || IsIgnoredByGit(fullPath, workspaceRoot, gitIgnorePatterns) {
					continue
				}
			} else {
				if IsIgnoredFileName(name) || IsIgnoredByGit(fullPath, workspaceRoot, gitIgnorePatterns) {
					continue
				}
			}
			validEntries = append(validEntries, entry)
		}

		sort.Slice(validEntries, func(i, j int) bool {
			if validEntries[i].IsDir() != validEntries[j].IsDir() {
				return validEntries[i].IsDir()
			}
			return strings.ToLower(validEntries[i].Name()) < strings.ToLower(validEntries[j].Name())
		})

		for i, entry := range validEntries {
			total++

			isLast := i == len(validEntries)-1
			connector := "├── "
			childPrefix := prefix + "│   "
			if isLast {
				connector = "└── "
				childPrefix = prefix + "    "
			}

			name := entry.Name()
			line := prefix + connector + name
			if entry.IsDir() {
				line += "/"
			}

			if total >= offset && !limitReached && !bytesReached {
				// Two independent budgets, first hit wins; complete lines only.
				if shown >= maxEntries {
					limitReached = true
				} else if bytesUsed+len(line)+1 > limits.MaxOutputBytes() {
					bytesReached = true
				} else {
					bytesUsed += len(line) + 1
					rendered = append(rendered, line)
					shown++
				}
			}

			if entry.IsDir() {
				walkTree(filepath.Join(currentDir, name), childPrefix, depth+1)
			}
		}
	}

	walkTree(dirPath, "", 1)

	if limitReached || bytesReached {
		next := shown + offset
		notice := file.SearchLimitNotice("list", shown, total, next)
		if bytesReached && !limitReached {
			notice = file.OutputLimitNotice("list", limits.MaxOutputBytes()/1024, shown, total, next)
		}
		rendered = append(rendered, strings.TrimRight(notice, "\n"))
	}

	return strings.Join(rendered, "\n"), nil
}
