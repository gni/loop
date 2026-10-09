package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

// DirectoryTreeLister is an optional hook allowing integration with higher-level directory tree formatters.
var DirectoryTreeLister func(dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error)

type readTool struct{}

// NewReadTool initializes the file reading tool.
func NewReadTool() domaintool.ToolExecutor {
	return &readTool{}
}

func (t *readTool) Name() string { return "read" }

func (t *readTool) PromptSnippet() string {
	return "Read file contents"
}

func (t *readTool) PromptGuidelines() []string {
	return []string{
		"Use 'read' to examine files instead of cat or sed in bash. Do not call 'read' on directory paths; use 'list' to inspect directory trees.",
		"In tool call arguments, always output 'path' first.",
	}
}

var readToolDef = domaintool.NewFunctionTool(
	"read",
	"Read file contents. Supports text files. Specify 'path' first before offset or limit.",
	map[string]domaintool.SchemaProp{
		"path":   domaintool.StringProp("Path to a specific file to read (relative or absolute). Specify this parameter first. Do not pass directory paths; use 'list' to inspect directory trees."),
		"offset": domaintool.NumberProp("Line number to start reading from (1-indexed). Optional"),
		"limit":  domaintool.NumberProp("Maximum number of lines to read. Optional"),
	},
	"path",
)

func (t *readTool) Definition() domaintool.Tool {
	return readToolDef
}

func (t *readTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path              string  `json:"path"`
		File              string  `json:"file"`
		FilePath          string  `json:"file_path"`
		FilePathCamel     string  `json:"filePath"`
		TargetFile        string  `json:"target_file"`
		TargetFileCamel   string  `json:"targetFile"`
		TargetFilePascal  string  `json:"TargetFile"`
		FileName          string  `json:"file_name"`
		FileNameCamel     string  `json:"fileName"`
		Filename          string  `json:"filename"`
		Target            string  `json:"target"`
		AbsolutePath      string  `json:"AbsolutePath"`
		AbsolutePathSnake string  `json:"absolute_path"`
		Offset            float64 `json:"offset"`
		Limit             float64 `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		var rawPath string
		if errStr := json.Unmarshal([]byte(arguments), &rawPath); errStr == nil && strings.TrimSpace(rawPath) != "" {
			args.Path = rawPath
		} else {
			trimmed := strings.TrimSpace(arguments)
			if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
				args.Path = trimmed
			}
		}
	}

	targetPath := extractFilePath(arguments,
		args.Path, args.FilePath, args.FilePathCamel,
		args.TargetFile, args.TargetFileCamel, args.TargetFilePascal,
		args.FileName, args.FileNameCamel, args.Filename,
		args.File, args.Target, args.AbsolutePath, args.AbsolutePathSnake,
	)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	args.Path = targetPath

	if args.Offset == 0 || args.Limit == 0 {
		var generic map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &generic); err == nil && generic != nil {
			for _, w := range []string{"input", "parameters", "arguments", "read", "params", "tool_input"} {
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

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	if hasIgnoredComponent(args.Path) {
		return "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", args.Path)
	}

	unlock := lockPath(safePath)
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
					relPath = args.Path + ext
				}
				entryHeader = fmt.Sprintf("[Notice: Resolved '%s' to '%s']\n\n", args.Path, relPath)
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
					relEntry = filepath.Join(args.Path, entryName)
				}
				entryHeader = fmt.Sprintf("[Notice: '%s' is a directory. Automatically reading package entrypoint '%s':]\n\n", args.Path, relEntry)
				break
			}
		}

		if !foundEntry {
			if DirectoryTreeLister != nil {
				tree, err := DirectoryTreeLister(safePath, ctx.GetWorkspaceRoot(), 2, 150)
				if err != nil {
					return "", fmt.Errorf("path '%s' is a directory and failed to list contents: %w", args.Path, err)
				}
				return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n%s", args.Path, tree), nil
			}
			return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n(directory)", args.Path), nil
		}
	}
	if info.Size() > 500*1024 { // 500KB limit
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is 500KB", info.Size())
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		fo.RecordRead(safePath, data)
	}

	// Check if file is binary
	if isBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files")
	}

	_, textWithoutBOM := splitBOM(data)
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
		limit = 1000 // default to 1000 lines so normal files are read completely in one call
	} else if limit > 2000 {
		limit = 2000 // cap maximum lines per read to 2000
	}

	end := offset + limit - 1
	truncated := false
	if end > len(lines) {
		end = len(lines)
	} else if end < len(lines) {
		truncated = true
	}

	var resultLines []string
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > 1000 {
			line = line[:1000] + " ... [line truncated: line is too long] ..."
		}
		resultLines = append(resultLines, line)
	}

	result := strings.Join(resultLines, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[Showing lines %d to %d of %d. Use read with offset=%d to view more]", offset, end, len(lines), end+1)
	}
	return entryHeader + result, nil
}
