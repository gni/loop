package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaintool "loop/pkg/domain/tool"
)

type writeTool struct{}

func NewWriteTool() domaintool.ToolExecutor {
	return &writeTool{}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Create or overwrite a file: {\"path\": \"path/to/file\", \"content\": \"...\"}")
}

func (t *writeTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'write' only for new files or complete rewrites.",
		"Request one 'write' at a time and inspect its result before requesting the next file. Do not batch writes in one response.",
	})
}

func (t *writeTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"write",
		domaintool.FormatToolDescription("write", "Create a new file or completely overwrite an existing file. In arguments JSON, ALWAYS specify 'path' first before 'content' (e.g. {\"path\": \"...\", \"content\": \"...\"}). Automatically creates parent directories."),
		map[string]domaintool.SchemaProp{
			"path":    domaintool.StringProp(domaintool.FormatParamDescription("write", "path", "Path to the target file. ALWAYS specify 'path' first before 'content'.")),
			"content": domaintool.StringProp(domaintool.FormatParamDescription("write", "content", "Complete content to write into the file. ALWAYS specify 'content' after 'path'.")),
		},
		"path", "content",
	)
}

func (t *writeTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	_ = json.Unmarshal([]byte(arguments), &args)

	targetPath := ExtractFilePath(arguments, args.Path)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	var content string
	if args.Content != nil {
		content = *args.Content
	} else {
		extracted, found := ExtractFileContent(arguments)
		if !found {
			return "", fmt.Errorf("missing required argument: content")
		}
		content = extracted
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		return "", err
	}

	unlock := LockPath(safePath)
	defer unlock()

	if placeholders := DetectOmissionPlaceholders(content); len(placeholders) > 0 {
		return "", fmt.Errorf("refusing to write file: detected code omission placeholder(s) like: %q. Please provide the complete file content without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
	}

	discardIfUncommitted := prepareSnapshotRollback(ctx, safePath)

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		if err := fo.CheckMutationAllowed(safePath, false); err != nil {
			discardIfUncommitted()
			return "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	if err := CommitFileMutation(ctx, safePath, []byte(content), 0644); err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), safePath), nil
}
