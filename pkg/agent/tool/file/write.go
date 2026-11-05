package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type writeTool struct{}

func NewWriteTool() domaintool.ToolExecutor {
	return &writeTool{}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Create or overwrite a file")
}

func (t *writeTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'write' only for new files or complete replacements.",
	})
}

func (t *writeTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"write",
		domaintool.FormatToolDescription("write", "Create or replace a file. Creates parent directories. Read existing files before overwriting; use distinct new filenames when names are unspecified. Independent writes may be batched; execution remains ordered. File observation requirements depend on configuration."),
		map[string]domaintool.SchemaProp{
			"path":    domaintool.StringProp(domaintool.FormatParamDescription("write", "path", "Path to the target file.")),
			"content": domaintool.StringProp(domaintool.FormatParamDescription("write", "content", "Complete content to write into the file.")),
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
	if strings.ContainsAny(targetPath, "*?[") {
		return "", fmt.Errorf("cannot write to wildcard path '%s': please provide an exact target file path", targetPath)
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

	observer, ok := ctx.(domaintool.FileObserver)
	if !ok {
		discardIfUncommitted()
		return "", fmt.Errorf("file observation guard is unavailable")
	}
	if err := observer.CheckMutationAllowed(safePath, false); err != nil {
		discardIfUncommitted()
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	if err := CommitFileMutation(ctx, safePath, []byte(content), 0644); err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	displayPath := targetPath
	if rel, err := filepath.Rel(ctx.GetWorkspaceRoot(), safePath); err == nil && !strings.HasPrefix(rel, "..") {
		displayPath = rel
	}
	msg := fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), displayPath)
	if warning := ValidateSyntax(safePath, []byte(content)); warning != "" {
		msg += warning
	}
	return msg, nil
}
