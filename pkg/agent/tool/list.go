package tool

import (
	"loop/pkg/agent/tool/search"
)

// NewListTool creates a new directory listing tool.
func NewListTool() ToolExecutor {
	return search.NewListTool()
}
