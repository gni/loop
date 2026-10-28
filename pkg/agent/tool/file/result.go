package file

import (
	"fmt"
	"strings"

	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

func (t *readTool) ExecuteResult(ctx domaintool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	path := ExtractFilePath(arguments)
	lower := strings.ToLower(path)
	image := false
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		image = image || strings.HasSuffix(lower, extension)
	}
	if !image {
		output, err := t.Execute(ctx, arguments)
		return domaintool.ToolResult{Output: output}, err
	}
	path, err := ctx.SafePath(path)
	if err != nil {
		return domaintool.ToolResult{}, err
	}
	block, err := message.ReadAttachment(path)
	if err != nil {
		return domaintool.ToolResult{}, err
	}
	if block.Type != "image" {
		return domaintool.ToolResult{}, fmt.Errorf("file is not a supported image")
	}
	return domaintool.ToolResult{Output: "Image: " + path, Content: []message.ContentBlock{block}}, nil
}
