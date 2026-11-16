package file

import (
	"fmt"
	"strings"

	"loop/pkg/domain/diagnostic"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
)

type editTool struct{}

func NewEditTool() domaintool.ToolExecutor {
	return &editTool{}
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Edit a file using exact text replacements")
}

func (t *editTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'edit' for targeted changes. Match against current file contents with unique, concise blocks (2-5 lines).",
	})
}

func (t *editTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"edit",
		domaintool.FormatToolDescription("edit", "Edit a file using exact text replacement blocks. Matches unique blocks against current file contents."),
		map[string]domaintool.SchemaProp{
			"path": domaintool.StringProp(domaintool.FormatParamDescription("edit", "path", "Path to the file to edit.")),
			"updates": {
				Type:        "array",
				Description: domaintool.FormatParamDescription("edit", "updates", "One or more targeted replacements."),
				Items: &domaintool.SchemaProp{
					Type: "object",
					Properties: map[string]domaintool.SchemaProp{
						"oldText": domaintool.StringProp(domaintool.FormatParamDescription("edit", "oldText", "Exact unique current text copied from latest read (typically 2-5 lines).")),
						"newText": domaintool.StringProp(domaintool.FormatParamDescription("edit", "newText", "The replacement text for oldText.")),
					},
					Required: []string{"oldText", "newText"},
				},
			},
		},
		"path", "updates",
	)
}

func (t *editTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	result, err := t.ExecuteResult(ctx, arguments)
	return result.Output, err
}

func (t *editTool) ExecuteResult(ctx domaintool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	observed := &evidence.Record{Kind: "file"}
	output, err := t.execute(ctx, arguments, observed)
	observed.Outcome = evidence.Succeeded
	if err != nil {
		observed.Outcome = evidence.Failed
		if mutationCommitted(err, observed.Target) {
			changed := true
			observed.Changed = &changed
		}
	}
	return domaintool.ToolResult{Output: output, IsError: err != nil, Evidence: observed}, err
}

func (t *editTool) execute(ctx domaintool.AgentContext, arguments string, observed *evidence.Record) (string, error) {
	targetPath, edits, err := ParseEditArguments(arguments)
	if err != nil {
		unchanged := false
		observed.Changed = &unchanged
		return "", &diagnostic.Error{Code: diagnostic.InvalidArguments, Target: "edit", Cause: err}
	}
	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		return "", err
	}

	observed.Target = safePath

	unlock, err := LockPathContext(ctx.Context(), safePath)
	if err != nil {
		return "", err
	}
	defer unlock()

	for i, edit := range edits {
		if edit.Path == "" {
			continue
		}
		path, err := ctx.SafePath(edit.Path)
		if err != nil {
			return "", err
		}
		if path != safePath {
			return "", &diagnostic.Error{Code: diagnostic.InvalidArguments, Target: targetPath, Cause: fmt.Errorf("edit[%d]: all replacements must target the same file", i)}
		}
	}

	discardIfUncommitted := prepareSnapshotRollback(ctx, safePath)

	observer, ok := ctx.(domaintool.FileObserver)
	if !ok {
		discardIfUncommitted()
		return "", fmt.Errorf("file observation guard is unavailable")
	}
	if err := observer.CheckMutationAllowed(safePath, true); err != nil {
		discardIfUncommitted()
		return "", err
	}

	data, err := storage.ReadFileContext(ctx.Context(), safePath, int64(limits.For(ctx).MaxReadFileBytes))
	if err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	if IsBinary(data) {
		discardIfUncommitted()
		return "", fmt.Errorf("cannot edit binary file; provide a text file")
	}
	hasBOM, rawStr := SplitBOM(data)
	content := rawStr
	initialContent := content

	content, err = ApplyEdits(ctx.Context(), initialContent, edits, limits.For(ctx).MaxReadFileBytes)
	if err != nil {
		discardIfUncommitted()
		return "", diagnostic.WithTarget(err, targetPath)
	}
	if content == initialContent {
		changed := false
		observed.Changed = &changed
		warning, validationErr := inspectCandidate(ctx, safePath, data, observed)
		discardIfUncommitted()
		return "No changes made: requested edits leave the file unchanged." + warning, validationErr
	}
	diff, err := GenerateDisplayDiffContext(ctx.Context(), strings.ReplaceAll(initialContent, "\r\n", "\n"), strings.ReplaceAll(content, "\r\n", "\n"), 3)
	if err != nil {
		discardIfUncommitted()
		return "", err
	}
	finalBytes := []byte(content)
	if hasBOM {
		finalBytes = append([]byte{0xef, 0xbb, 0xbf}, finalBytes...)
	}
	if len(finalBytes) > limits.For(ctx).MaxReadFileBytes {
		discardIfUncommitted()
		return "", fmt.Errorf("edited file exceeds size limit of %d bytes", limits.For(ctx).MaxReadFileBytes)
	}
	warning, err := inspectCandidate(ctx, safePath, finalBytes, observed)
	if err != nil {
		discardIfUncommitted()
		return "", err
	}
	if err := CommitFileMutation(ctx, safePath, finalBytes, 0644); err != nil {
		if !mutationCommitted(err, safePath) {
			discardIfUncommitted()
		}
		return "", fmt.Errorf("failed to write modified content back: %w", err)
	}

	changed := true
	observed.Changed = &changed
	if warning != "" {
		diff += warning
	}

	return diff, nil
}
