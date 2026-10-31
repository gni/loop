package tool

import (
	"context"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/agent/tool/search"
)

func init() {
	filetool.DirectoryTreeLister = func(ctx context.Context, path, root string, depth, entries int) (string, error) {
		return search.ListDirectoryTreeContext(ctx, path, root, depth, entries, 1)
	}
}

// NewReadTool initializes the file reading tool.
func NewReadTool() ToolExecutor {
	return filetool.NewReadTool()
}

// NewWriteTool initializes the file writing tool.
func NewWriteTool() ToolExecutor {
	return filetool.NewWriteTool()
}

// NewEditTool initializes the targeted file editing tool.
func NewEditTool() ToolExecutor {
	return filetool.NewEditTool()
}

// SanitizeUTF8 replaces invalid UTF-8 byte sequences with spaces.
func SanitizeUTF8(data []byte) string {
	return filetool.SanitizeUTF8(data)
}
