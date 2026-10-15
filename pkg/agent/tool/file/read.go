package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

const readDefaultLimit = 1000

var DirectoryTreeLister func(dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error)

type readTool struct{}

func NewReadTool() domaintool.ToolExecutor {
	return &readTool{}
}

func (t *readTool) Name() string { return "read" }

func (t *readTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Read file contents")
}

func (t *readTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Do not call 'read' on directory paths; use 'list' to inspect directory trees.",
	})
}

func (t *readTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"read",
		domaintool.FormatToolDescription("read", "Read file contents. Supports text files. Specify 'path' first before offset or limit. Results are truncated to 2000 lines or 50KB (whichever is hit first), never mid-line; each line is capped at 1000 characters. Truncation notices state the exact next offset and the total line count, so use offset/limit to page through the rest until complete."),
		map[string]domaintool.SchemaProp{
			"path":   domaintool.StringProp(domaintool.FormatParamDescription("read", "path", "Path to a specific file to read (relative or absolute). Specify this parameter first. Do not pass directory paths; use 'list' to inspect directory trees.")),
			"offset": domaintool.NumberProp(domaintool.FormatParamDescription("read", "offset", "Line number to start reading from (1-indexed). Optional")),
			"limit":  domaintool.NumberProp(domaintool.FormatParamDescription("read", "limit", "Maximum number of lines to read. Optional")),
		},
		"path",
	)
}

func (t *readTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path   string  `json:"path"`
		Offset float64 `json:"offset"`
		Limit  float64 `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		var rawPath string
		if errStr := json.Unmarshal([]byte(arguments), &rawPath); errStr == nil && strings.TrimSpace(rawPath) != "" {
			args.Path = rawPath
		}
	}

	targetPath := ExtractFilePath(arguments, args.Path)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	if args.Offset == 0 || args.Limit == 0 {
		var generic map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &generic); err == nil && generic != nil {
			for _, w := range wrapperKeys {
				if sub, ok := generic[w].(map[string]interface{}); ok {
					if args.Offset == 0 {
						if off, ok := sub["offset"].(float64); ok {
							args.Offset = off
						}
					}
					if args.Limit == 0 {
						if lim, ok := sub["limit"].(float64); ok {
							args.Limit = lim
						}
					}
				}
			}
		}
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		return "", err
	}

	if HasIgnoredComponent(targetPath) {
		return "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", targetPath)
	}

	unlock := LockPath(safePath)
	defer unlock()

	var entryHeader string
	info, err := os.Stat(safePath)
	if err != nil {
		found := false
		for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go", ".json", ".md", ".yaml", ".yml"} {
			candidate := safePath + ext
			if cInfo, cErr := os.Stat(candidate); cErr == nil && !cInfo.IsDir() {
				safePath = candidate
				info = cInfo
				found = true
				relPath, _ := filepath.Rel(ctx.GetWorkspaceRoot(), candidate)
				if relPath == "" {
					relPath = targetPath + ext
				}
				entryHeader = fmt.Sprintf("[Notice: Resolved '%s' to '%s']\n\n", targetPath, relPath)
				break
			}
		}
		if !found {
			for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go"} {
				if strings.HasSuffix(safePath, ext) {
					trimmed := strings.TrimSuffix(safePath, ext)
					if dInfo, dErr := os.Stat(trimmed); dErr == nil && dInfo.IsDir() {
						safePath = trimmed
						info = dInfo
						found = true
						break
					}
				}
			}
		}
		if !found {
			return "", fmt.Errorf("failed to read file info: %w", err)
		}
	}

	if info.IsDir() {
		foundEntry := false
		for _, entryName := range []string{"__init__.py", "index.ts", "index.js", "index.tsx", "index.jsx", "main.go"} {
			entryPath := filepath.Join(safePath, entryName)
			if entryInfo, err := os.Stat(entryPath); err == nil && !entryInfo.IsDir() && entryInfo.Size() > 0 {
				safePath = entryPath
				info = entryInfo
				foundEntry = true
				relEntry, _ := filepath.Rel(ctx.GetWorkspaceRoot(), entryPath)
				if relEntry == "" {
					relEntry = filepath.Join(targetPath, entryName)
				}
				entryHeader = fmt.Sprintf("[Notice: '%s' is a directory. Automatically reading package entrypoint '%s':]\n\n", targetPath, relEntry)
				break
			}
		}

		if !foundEntry {
			if DirectoryTreeLister != nil {
				tree, err := DirectoryTreeLister(safePath, ctx.GetWorkspaceRoot(), 2, 150)
				if err != nil {
					return "", fmt.Errorf("path '%s' is a directory and failed to list contents: %w", targetPath, err)
				}
				return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n%s", targetPath, tree), nil
			}
			return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n(directory)", targetPath), nil
		}
	}

	maxReadBytes := limits.MaxReadFileBytes()
	if info.Size() > int64(maxReadBytes) {
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is %d bytes", info.Size(), maxReadBytes)
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		fo.RecordRead(safePath, data)
	}

	if IsBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files")
	}

	_, textWithoutBOM := SplitBOM(data)
	contentStr := SanitizeUTF8([]byte(textWithoutBOM))
	if len(data) == 0 || strings.TrimSpace(contentStr) == "" {
		return entryHeader + "(empty file)", nil
	}

	lines := strings.Split(contentStr, "\n")
	offset := int(args.Offset)
	if offset <= 0 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = readDefaultLimit
	} else if limit > limits.MaxOutputLines() {
		limit = limits.MaxOutputLines()
	}

	end := offset + limit - 1
	if end > len(lines) {
		end = len(lines)
	}
	window := lines[offset-1 : end]
	cut := TruncateHead(window, limit, limits.MaxOutputBytes())
	if cut.ShownLines == 0 {
		return entryHeader + ReadFirstLineOverBudgetNotice(limits.MaxOutputBytes(), offset, targetPath), nil
	}

	lineCap := limits.ReadMaxLineLength()
	resultLines := make([]string, 0, cut.ShownLines)
	for _, line := range window[:cut.ShownLines] {
		resultLines = append(resultLines, TruncateLine(line, lineCap))
	}

	result := strings.Join(resultLines, "\n")
	shownEnd := offset + cut.ShownLines - 1
	if cut.Truncated {
		result += ReadTruncationNotice(offset, shownEnd, len(lines), shownEnd+1)
	} else if end < len(lines) {
		result += ReadMoreLinesNotice(len(lines)-end, len(lines), end+1)
	}
	return entryHeader + result, nil
}