package file

import (
	"encoding/base64"
	"fmt"
	"strings"

	"loop/pkg/domain/ignore"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

func (t *readTool) ExecuteResult(ctx domaintool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	if err := ctx.Context().Err(); err != nil {
		return domaintool.ToolResult{IsError: true}, err
	}
	path := ExtractFilePath(arguments)
	lower := strings.ToLower(path)
	image := false
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		image = image || strings.HasSuffix(lower, extension)
	}
	if !image {
		output, err := t.Execute(ctx, arguments)
		return domaintool.ToolResult{Output: output, IsError: err != nil}, err
	}
	safePath, err := ctx.SafePath(path)
	if err != nil {
		return domaintool.ToolResult{IsError: true}, err
	}
	if ignore.HasComponent(safePath, ctx.GetWorkspaceRoot(), false) {
		return domaintool.ToolResult{IsError: true}, fmt.Errorf("cannot read ignored path %s", path)
	}
	// Match text-read limits and observe the exact bytes sent to the model.
	unlock := LockPath(safePath)
	block, err := message.ReadAttachment(safePath)
	unlock()
	if err != nil || block.Type != "image" {
		output, execErr := t.Execute(ctx, arguments)
		return domaintool.ToolResult{Output: output, IsError: execErr != nil}, execErr
	}
	if err := ctx.Context().Err(); err != nil {
		return domaintool.ToolResult{IsError: true}, err
	}
	data, err := base64.StdEncoding.DecodeString(block.Data)
	if err != nil {
		return domaintool.ToolResult{IsError: true}, fmt.Errorf("decode image observation: %w", err)
	}
	if len(data) > limits.For(ctx).MaxReadFileBytes {
		return domaintool.ToolResult{IsError: true}, fmt.Errorf("image exceeds read byte limit")
	}
	if observer, ok := ctx.(domaintool.FileObserver); ok {
		observer.RecordRead(safePath, data)
	}
	return domaintool.ToolResult{Output: fmt.Sprintf("Read image file %s [%s]", path, block.MimeType), Content: []message.ContentBlock{block}}, nil
}
