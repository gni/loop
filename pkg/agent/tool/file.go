package tool

import (
	filetool "loop/pkg/agent/tool/file"
)

func init() {
	filetool.DirectoryTreeLister = ListDirectoryTree
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
