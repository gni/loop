package file

import (
	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

// Text and image reads share argument checks, path resolution, limits, locking
// and observation. Detect image data from its bytes rather than its extension.
func (t *readTool) ExecuteResult(ctx domaintool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	var blocks []message.ContentBlock
	output, err := t.executeRead(ctx, arguments, &blocks)
	return domaintool.ToolResult{Output: output, Content: blocks, IsError: err != nil}, err
}
