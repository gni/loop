package file

import (
	"encoding/json"
	"fmt"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type prepareMutationTool struct{}

func NewPrepareMutationTool() domaintool.ToolExecutor {
	return &prepareMutationTool{}
}

func (t *prepareMutationTool) Name() string { return "prepare_mutation" }

func (t *prepareMutationTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Prepare the path of a new file before writing its content. Existing files are authorized by a successful read.")
}

func (t *prepareMutationTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Existing files: read the exact file once, then write or edit directly. New files: call prepare_mutation with operation write before generating content. A repeated prepare after a successful read or write is unnecessary.",
	})
}

func (t *prepareMutationTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"prepare_mutation",
		domaintool.FormatToolDescription("prepare_mutation", "Validate a new-file path without generating file content. Existing files become writable or editable after a successful read."),
		map[string]domaintool.SchemaProp{
			"path":      domaintool.StringProp("Exact path to the file, relative to workspace or absolute within the workspace."),
			"operation": {Type: "string", Description: "Operation to authorize.", Enum: []string{"write", "edit"}},
		},
		"path", "operation",
	)
}

func (t *prepareMutationTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path      string `json:"path"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid prepare_mutation arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("prepare_mutation requires a nonempty path")
	}
	if args.Operation != "write" && args.Operation != "edit" {
		return "", fmt.Errorf("operation must be 'write' or 'edit'")
	}

	path, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}
	preparer, ok := ctx.(domaintool.MutationPreparer)
	if !ok {
		return "", fmt.Errorf("this context does not support mutation preparation")
	}
	unlock := LockPath(path)
	defer unlock()
	if err := preparer.PrepareMutation(path, args.Operation == "edit"); err != nil {
		return "", err
	}
	return fmt.Sprintf("ready: %s %s. Generate the mutation now using the exact same path and operation.", args.Operation, path), nil
}
