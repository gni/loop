package agent

import (
	"os"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/storage"
)

func PreviewToolDiff(a *Agent, toolName, arguments string) string {
	if a == nil {
		return ""
	}
	ctx := a.Context()
	if ctx.Err() != nil {
		return ""
	}
	maxBytes := a.GetLimits().MaxReadFileBytes
	// JSON escaping can expand a file body; bound argument parsing as well.
	if len(arguments) > maxBytes*8 {
		return ""
	}
	norm := tool.NormalizeName(toolName)
	if norm != "write" && norm != "edit" {
		return ""
	}
	targetPath := filetool.ExtractFilePath(arguments)
	var edits []filetool.ReplaceEdit
	if norm == "edit" {
		var err error
		targetPath, edits, err = filetool.ParseEditArguments(arguments)
		if err != nil {
			return ""
		}
	}
	if targetPath == "" {
		return ""
	}
	safePath, err := a.SafePath(targetPath)
	if err != nil {
		return ""
	}

	unlock, err := filetool.LockPathContext(ctx, safePath)
	if err != nil {
		return ""
	}
	existingBytes, readErr := storage.ReadFileContext(ctx, safePath, int64(maxBytes))
	unlock()
	if ctx.Err() != nil || (readErr != nil && !os.IsNotExist(readErr)) || filetool.IsBinary(existingBytes) {
		return ""
	}
	oldContent := ""
	if readErr == nil {
		_, rawStr := filetool.SplitBOM(existingBytes)
		oldContent = strings.ReplaceAll(rawStr, "\r\n", "\n")
	}

	if norm == "write" {
		newContent, ok := filetool.ExtractFileContent(arguments)
		if !ok || len(newContent) > maxBytes || len(filetool.DetectOmissionPlaceholders(newContent)) > 0 {
			return ""
		}
		diff, _ := filetool.GenerateDisplayDiffContext(ctx, oldContent, strings.ReplaceAll(newContent, "\r\n", "\n"), 3)
		return diff
	}

	if readErr != nil {
		return ""
	}
	for _, edit := range edits {
		if edit.Path == "" {
			continue
		}
		path, err := a.SafePath(edit.Path)
		if err != nil || path != safePath {
			return ""
		}
	}
	previewContent, err := filetool.ApplyEdits(ctx, oldContent, edits, maxBytes)
	if err != nil || previewContent == oldContent {
		return ""
	}
	// Execution restores the original encoding, so account for it in the limit.
	finalSize := len(previewContent)
	_, raw := filetool.SplitBOM(existingBytes)
	if filetool.DetectLineEnding(raw) == "\r\n" {
		finalSize += strings.Count(previewContent, "\n")
	}
	if hasBOM, _ := filetool.SplitBOM(existingBytes); hasBOM {
		finalSize += 3
	}
	if finalSize > maxBytes {
		return ""
	}
	diff, _ := filetool.GenerateDisplayDiffContext(ctx, oldContent, previewContent, 3)
	return diff
}
