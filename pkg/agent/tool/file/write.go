package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaintool "loop/pkg/domain/tool"
)

type writeTool struct{}

// NewWriteTool initializes the file writing tool.
func NewWriteTool() domaintool.ToolExecutor {
	return &writeTool{}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Create or overwrite complete files")
}

func (t *writeTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'write' only for new files or complete rewrites. Never use after an edit mismatch.",
		"Specify the target file in 'path' and the complete file contents in 'content'.",
		"In tool call arguments, always output 'path' before 'content' to enable real-time streaming preview and syntax highlighting.",
	})
}

func (t *writeTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"write",
		domaintool.FormatToolDescription("write", "Create a new file or completely overwrite an existing file. Specify 'path' first before 'content'. Automatically creates parent directories."),
		map[string]domaintool.SchemaProp{
			"path":    domaintool.StringProp(domaintool.FormatParamDescription("write", "path", "Path to the target file. Specify 'path' first before 'content'.")),
			"content": domaintool.StringProp(domaintool.FormatParamDescription("write", "content", "Complete content to write into the file. Specify 'content' after 'path'.")),
		},
		"path", "content",
	)
}

func (t *writeTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path              string  `json:"path"`
		File              string  `json:"file"`
		FilePath          string  `json:"file_path"`
		FilePathCamel     string  `json:"filePath"`
		Target            string  `json:"target"`
		TargetFile        string  `json:"target_file"`
		TargetFileCamel   string  `json:"targetFile"`
		TargetFilePascal  string  `json:"TargetFile"`
		FileName          string  `json:"file_name"`
		FileNameCamel     string  `json:"fileName"`
		Filename          string  `json:"filename"`
		WritePath         string  `json:"write_path"`
		WritePathCamel    string  `json:"writePath"`
		AbsolutePath      string  `json:"AbsolutePath"`
		AbsolutePathSnake string  `json:"absolute_path"`
		Content           *string `json:"content"`
		CodeContent       *string `json:"code_content"`
		CodeContentCamel  *string `json:"codeContent"`
		CodeContentPascal *string `json:"CodeContent"`
		WriteContent      *string `json:"write_content"`
		Text              *string `json:"text"`
		Body              *string `json:"body"`
		Data              *string `json:"data"`
	}
	_ = json.Unmarshal([]byte(arguments), &args)

	targetPath := extractFilePath(arguments,
		args.Path, args.FilePath, args.FilePathCamel,
		args.TargetFile, args.TargetFileCamel, args.TargetFilePascal,
		args.FileName, args.FileNameCamel, args.Filename,
		args.File, args.Target, args.WritePath, args.WritePathCamel,
		args.AbsolutePath, args.AbsolutePathSnake,
	)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	args.Path = targetPath

	var candidateContents []string
	contentProvided := false
	for _, ptr := range []*string{args.Content, args.CodeContent, args.CodeContentCamel, args.CodeContentPascal, args.WriteContent, args.Text, args.Body, args.Data} {
		if ptr != nil {
			candidateContents = append(candidateContents, *ptr)
			contentProvided = true
			break
		}
	}

	var content string
	if contentProvided {
		content = candidateContents[0]
	} else {
		extracted, found := extractFileContent(arguments)
		if !found {
			return "", fmt.Errorf("missing required argument: content")
		}
		content = extracted
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	unlock := lockPath(safePath)
	defer unlock()

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		if err := fo.CheckMutationAllowed(safePath, false); err != nil {
			return "", err
		}
	}

	// Code Omission Protection
	if placeholders := DetectOmissionPlaceholders(content); len(placeholders) > 0 {
		return "", fmt.Errorf("refusing to write file: detected code omission placeholder(s) like: %q. Please provide the complete file content without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	useAtomic := true
	if cp, ok := ctx.(interface{ IsAtomicWritesEnabled() bool }); ok {
		useAtomic = cp.IsAtomicWritesEnabled()
	}
	if useAtomic {
		err = atomicWriteFile(safePath, []byte(content), 0644)
	} else {
		err = os.WriteFile(safePath, []byte(content), 0644)
	}
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	if fo, ok := ctx.(domaintool.FileObserver); ok {
		fo.RecordMutation(safePath, []byte(content))
	}
	ctx.ReloadSkills()
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), args.Path), nil
}
