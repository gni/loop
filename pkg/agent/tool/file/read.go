package file

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/domain/ignore"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

const readDefaultLimit = 2000

var (
	DirectoryTreeLister func(ctx context.Context, dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error)

	fallbackFileExts = [...]string{".py", ".ts", ".js", ".tsx", ".jsx", ".go", ".json", ".md", ".yaml", ".yml"}
	fallbackDirExts  = [...]string{".py", ".ts", ".js", ".tsx", ".jsx", ".go"}
	packageEntries   = [...]string{"__init__.py", "index.ts", "index.js", "index.tsx", "index.jsx", "main.go"}
)

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
		"Use 'read' to examine files instead of cat or sed. Use 'offset' and 'limit' to inspect specific line ranges in large files.",
		"Use 'list_directory' for directories.",
	})
}

func (t *readTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"read",
		domaintool.FormatToolDescription("read", "Read text or image files. Text output follows the configured page budget; use offset and limit to paginate files and saved tool output. Image files (.png, .jpg, .gif, .webp) return visual content blocks."),
		map[string]domaintool.SchemaProp{
			"path":   domaintool.StringProp(domaintool.FormatParamDescription("read", "path", "Path to a specific file to read (relative or absolute). Do not pass directory paths; use 'list_directory' to inspect directory trees.")),
			"offset": domaintool.NumberProp(domaintool.FormatParamDescription("read", "offset", "Line number to start reading from (1-indexed). Optional")),
			"limit":  domaintool.NumberProp(domaintool.FormatParamDescription("read", "limit", "Maximum number of lines to read. Optional")),
		},
		"path",
	)
}

func relPathOrFallback(workspaceRoot, candidate, fallback string) string {
	if rel, err := filepath.Rel(workspaceRoot, candidate); err == nil && rel != "" {
		return rel
	}
	return fallback
}

func resolveMissingReadTarget(safePath, targetPath, workspaceRoot string) (string, os.FileInfo, string, bool) {
	for _, ext := range fallbackFileExts {
		candidate := safePath + ext
		if cInfo, cErr := os.Stat(candidate); cErr == nil && !cInfo.IsDir() {
			relPath := relPathOrFallback(workspaceRoot, candidate, targetPath+ext)
			header := fmt.Sprintf("[Notice: Resolved '%s' to '%s']\n\n", targetPath, relPath)
			return candidate, cInfo, header, true
		}
	}
	for _, ext := range fallbackDirExts {
		if strings.HasSuffix(safePath, ext) {
			trimmed := strings.TrimSuffix(safePath, ext)
			if dInfo, dErr := os.Stat(trimmed); dErr == nil && dInfo.IsDir() {
				return trimmed, dInfo, "", true
			}
		}
	}
	return safePath, nil, "", false
}

func resolveDirEntrypoint(safePath, targetPath, workspaceRoot string) (string, os.FileInfo, string, bool) {
	for _, entryName := range packageEntries {
		entryPath := filepath.Join(safePath, entryName)
		if entryInfo, err := os.Stat(entryPath); err == nil && !entryInfo.IsDir() && entryInfo.Size() > 0 {
			relEntry := relPathOrFallback(workspaceRoot, entryPath, filepath.Join(targetPath, entryName))
			header := fmt.Sprintf("[Notice: '%s' is a directory. Automatically reading package entrypoint '%s':]\n\n", targetPath, relEntry)
			return entryPath, entryInfo, header, true
		}
	}
	return safePath, nil, "", false
}

func (t *readTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	result, err := t.ExecuteResult(ctx, arguments)
	return result.Output, err
}

func (t *readTool) executeRead(ctx domaintool.AgentContext, arguments string, blocks *[]message.ContentBlock) (string, error) {
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	var args struct {
		Path   string  `json:"path"`
		Offset float64 `json:"offset"`
		Limit  float64 `json:"limit"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(path string) { args.Path = path }); err != nil {
		return "", err
	}

	targetPath := ExtractFilePath(arguments, args.Path)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	if args.Offset == 0 || args.Limit == 0 {
		wrapped := ExtractWrappedFloats(arguments, "offset", "limit")
		if args.Offset == 0 {
			args.Offset = wrapped["offset"]
		}
		if args.Limit == 0 {
			args.Limit = wrapped["limit"]
		}
	}

	offset, err := domaintool.IntegerArgument("offset", args.Offset)
	if err != nil {
		return "", err
	}
	limit, err := domaintool.IntegerArgument("limit", args.Limit)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(targetPath, "*?") {
		return "", fmt.Errorf("path '%s' contains glob wildcards. The 'read' tool requires a specific file path; use the 'find_files' tool instead to search files matching this pattern (e.g. find_files(pattern: '%s'))", targetPath, targetPath)
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		return "", err
	}

	if ignore.HasComponent(safePath, ctx.GetWorkspaceRoot(), false) {
		return "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", targetPath)
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	var entryHeader string
	info, err := os.Stat(safePath)
	if err != nil {
		resolvedPath, resolvedInfo, header, found := resolveMissingReadTarget(safePath, targetPath, workspaceRoot)
		if !found {
			return "", fmt.Errorf("failed to read file info: %w", err)
		}
		safePath = resolvedPath
		info = resolvedInfo
		entryHeader = header
	}

	if info.IsDir() {
		resolvedPath, resolvedInfo, header, foundEntry := resolveDirEntrypoint(safePath, targetPath, workspaceRoot)
		if !foundEntry {
			if DirectoryTreeLister != nil {
				tree, treeErr := DirectoryTreeLister(ctx.Context(), safePath, workspaceRoot, 2, 150)
				if treeErr != nil {
					return "", fmt.Errorf("path '%s' is a directory and failed to list contents: %w", targetPath, treeErr)
				}
				return fmt.Sprintf("[Path '%s' is a directory. Use 'list_directory' to view directories, or call 'read' with a specific file path to view its content:]\n\n%s", targetPath, tree), nil
			}
			return fmt.Sprintf("[Path '%s' is a directory. Use 'list_directory' to view directories, or call 'read' with a specific file path to view its content:]\n\n(directory)", targetPath), nil
		}
		safePath = resolvedPath
		info = resolvedInfo
		entryHeader = header
	}

	// The fallback extension or directory entrypoint can be a symlink even when
	// the originally requested path was safe. Validate the final target again.
	validatedPath, err := ctx.SafePath(safePath)
	if err != nil {
		return "", err
	}
	safePath = validatedPath
	if ignore.HasComponent(safePath, workspaceRoot, false) {
		return "", fmt.Errorf("cannot read ignored path %s", targetPath)
	}
	unlock, err := LockPathContext(ctx.Context(), safePath)
	if err != nil {
		return "", err
	}
	defer unlock()

	bounds := limits.For(ctx)
	offset = max(1, offset)
	if limit == 0 {
		limit = readDefaultLimit
	}
	limit = min(limit, bounds.MaxOutputLines)
	budget := limits.ToolOutputBytes(ctx)
	if relative, err := filepath.Rel(workspaceRoot, safePath); err == nil && storage.IsSpillPath(relative) {
		return readSpillPage(ctx.Context(), workspaceRoot, relative, entryHeader, offset, limit, budget, bounds.ReadMaxLineLength)
	}
	maxReadBytes := bounds.MaxReadFileBytes
	if info.Size() > int64(maxReadBytes) {
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is %d bytes", info.Size(), maxReadBytes)
	}

	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	data, err := storage.ReadFileContext(ctx.Context(), safePath, int64(maxReadBytes))
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		fo.RecordRead(safePath, data)
	}

	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		block, err := message.AttachmentFromBytes(data)
		if err != nil {
			return "", err
		}
		*blocks = []message.ContentBlock{block}
		return entryHeader + fmt.Sprintf("Read image file %s [%s, %d bytes]", targetPath, mime, len(data)), nil
	}
	if IsBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files and images")
	}

	_, textWithoutBOM := SplitBOM(data)
	contentStr := SanitizeUTF8([]byte(textWithoutBOM))
	if len(data) == 0 || strings.TrimSpace(contentStr) == "" {
		return renderReadPage(entryHeader, []string{"(empty file)"}, offset, 1, budget)
	}

	lines := strings.Split(contentStr, "\n")
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", offset, len(lines))
	}
	end := min(len(lines), offset+limit-1)
	window := make([]string, 0, end-offset+1)
	for _, line := range lines[offset-1 : end] {
		window = append(window, TruncateLine(line, bounds.ReadMaxLineLength))
	}
	return renderReadPage(entryHeader, window, offset, len(lines), budget)
}
