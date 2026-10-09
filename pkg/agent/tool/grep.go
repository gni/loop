package tool

import (
	"loop/pkg/agent/tool/search"
)

// NewGrepTool initializes a new recursive content search tool.
func NewGrepTool() ToolExecutor {
	return search.NewGrepTool()
}
