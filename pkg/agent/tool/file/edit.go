package file

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type editTool struct{}

func NewEditTool() domaintool.ToolExecutor {
	return &editTool{}
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "<edit path=\"path/to/file\">\n<old>...</old>\n<new>...</new>\n</edit>")
}

func (t *editTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Use 'edit' for precise changes: <edit path=\"path/to/file\">\n<old>\nexact old code\n</old>\n<new>\nexact new code\n</new>\n</edit>",
		"Keep old text minimal (typically 2-5 lines).",
		"If edit reports a mismatch, read the latest file and retry a smaller exact unique block. Never recover by overwriting the existing file with write.",
	})
}

func (t *editTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"edit",
		domaintool.FormatToolDescription("edit", "Edit a file using exact text replacement blocks. Matches unique blocks against current file contents."),
		map[string]domaintool.SchemaProp{
			"path": domaintool.StringProp(domaintool.FormatParamDescription("edit", "path", "Path to the file to edit. Always specify path first.")),
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

func tryApplyNonOverlappingBatch(initialContent string, edits []ReplaceEdit) (string, bool) {
	if len(edits) <= 1 {
		return "", false
	}
	matches := make([]*textMatch, len(edits))
	for i := range edits {
		m, err := locateEditMatch(initialContent, edits[i].OldText, edits[i].NewText)
		if err != nil || m == nil {
			return "", false
		}
		matches[i] = m
	}

	for a := 0; a < len(matches); a++ {
		for b := a + 1; b < len(matches); b++ {
			if matches[a].startByte < matches[b].endByte && matches[b].startByte < matches[a].endByte {
				return "", false
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].startByte > matches[j].startByte
	})

	content := initialContent
	changed := false
	for _, m := range matches {
		updated := content[:m.startByte] + m.newText + content[m.endByte:]
		if updated != content {
			changed = true
		}
		content = updated
	}
	return content, changed
}

func (t *editTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path         string        `json:"path"`
		Updates      []ReplaceEdit `json:"updates"`
		Edits        []ReplaceEdit `json:"edits"`
		Replacements []ReplaceEdit `json:"replacements"`
		ReplaceEdit
	}
	unmarshalErr := json.Unmarshal([]byte(arguments), &args)

	var topEdits []ReplaceEdit
	if topOld := args.ReplaceEdit.EffectiveOldText(); topOld != "" {
		topEdits = append(topEdits, ReplaceEdit{OldText: topOld, NewText: args.ReplaceEdit.EffectiveNewText()})
	}
	edits := ExtractEdits(arguments, args.Updates, args.Edits, args.Replacements, topEdits)

	candidatePaths := make([]string, 0, 1+len(edits)*4)
	if args.Path != "" {
		candidatePaths = append(candidatePaths, args.Path)
	}
	for _, e := range edits {
		candidatePaths = append(candidatePaths, e.Path, e.File, e.FilePath, e.TargetFile)
	}

	targetPath := ExtractFilePath(arguments, candidatePaths...)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}

	if len(edits) == 0 {
		if unmarshalErr != nil {
			return "", fmt.Errorf("invalid arguments: %w", unmarshalErr)
		}
		return "", fmt.Errorf("no edits specified to apply")
	}

	safePath, err := ctx.SafePath(targetPath)
	if err != nil {
		return "", err
	}

	unlock := LockPath(safePath)
	defer unlock()

	for i := range edits {
		edit := &edits[i]
		if effOld := edit.EffectiveOldText(); effOld != "" {
			edit.OldText = effOld
		}
		if effNew := edit.EffectiveNewText(); effNew != "" {
			edit.NewText = effNew
		}
		edit.OldText = strings.ReplaceAll(edit.OldText, "\r\n", "\n")
		edit.NewText = strings.ReplaceAll(edit.NewText, "\r\n", "\n")
		if placeholders := DetectOmissionPlaceholders(edit.NewText); len(placeholders) > 0 {
			return "", fmt.Errorf("refusing to edit file: detected code omission placeholder(s) in replacement text: %q. Please provide the complete new code replacement block without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
		}
	}

	discardIfUncommitted := prepareSnapshotRollback(ctx, safePath)

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		if err := fo.CheckMutationAllowed(safePath, true); err != nil {
			discardIfUncommitted()
			return "", err
		}
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	hasBOM, rawStr := SplitBOM(data)
	originalEnding := DetectLineEnding(rawStr)
	content := strings.ReplaceAll(rawStr, "\r\n", "\n")
	initialContent := content

	var diffBuilder strings.Builder
	contentChanged := false

	if batchContent, ok := tryApplyNonOverlappingBatch(initialContent, edits); ok {
		content = batchContent
		contentChanged = true
	} else {
		for i := range edits {
			edit := &edits[i]
			m, err := locateEditMatch(content, edit.OldText, edit.NewText)
			if err != nil {
				discardIfUncommitted()
				return "", fmt.Errorf("edit[%d]: %w", i, err)
			}
			if m == nil {
				if replacementAlreadyApplied(content, edit.NewText) {
					diffBuilder.WriteString(fmt.Sprintf("edit[%d]: requested replacement already present; file unchanged\n", i))
					continue
				}
				discardIfUncommitted()
				if closestLine := findClosestLineMatch(content, edit.OldText); closestLine > 0 {
					snippet := renderLineSnippet(strings.Split(content, "\n"), closestLine, 3)
					return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nDiagnostic Hint: A matching block was detected around line %d:\n---\n%s\n---\nTip: Notice the exact indentation/whitespace above. You can copy the exact block into 'oldText' without needing to call 'read'.", i, targetPath, closestLine, snippet)
				}
				return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nRecommendation: Read the file again using 'read' to verify its current contents, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write.", i, targetPath)
			}

			updatedContent := content[:m.startByte] + m.newText + content[m.endByte:]
			if updatedContent != content {
				contentChanged = true
			}
			content = updatedContent
		}
	}

	if !contentChanged {
		discardIfUncommitted()
		return diffBuilder.String(), nil
	}

	diffBuilder.WriteString(GenerateDisplayDiff(initialContent, content, 3))
	finalStr := content
	if originalEnding == "\r\n" {
		finalStr = strings.ReplaceAll(finalStr, "\n", "\r\n")
	}
	finalBytes := []byte(finalStr)
	if hasBOM {
		finalBytes = append([]byte{0xef, 0xbb, 0xbf}, finalBytes...)
	}
	if err := CommitFileMutation(ctx, safePath, finalBytes, 0644); err != nil {
		discardIfUncommitted()
		return "", fmt.Errorf("failed to write modified content back: %w", err)
	}

	return diffBuilder.String(), nil
}
