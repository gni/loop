package tool

import (
	"loop/pkg/agent/tool/bash"
)

// NewBashTool creates a new bash command executor.
func NewBashTool() ToolExecutor {
	return bash.NewBashTool()
}
