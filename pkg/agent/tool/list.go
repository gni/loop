package tool

import (
	"loop/pkg/agent/tool/search"
)

// NewListTool creates a new directory listing tool.
func NewListTool() ToolExecutor {
	return search.NewListTool()
}

// ListDirectoryTree traverses and renders a clean visual ASCII tree of the directory.
func ListDirectoryTree(dirPath string, workspaceRoot string, maxDepth int, maxEntries int) (string, error) {
	return search.ListDirectoryTree(dirPath, workspaceRoot, maxDepth, maxEntries)
}
