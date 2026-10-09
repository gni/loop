package tool

import (
	"loop/pkg/agent/tool/search"
)

// NewFindTool creates a new file finder tool.
func NewFindTool() ToolExecutor {
	return search.NewFindTool()
}
