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

// NewEditTool initializes the targeted file editing tool.
func NewEditTool() domaintool.ToolExecutor {
	return &editTool{}
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}

func (t *editTool) PromptGuidelines() []string {
	return []string{
		"Use 'edit' for precise changes (updates[].oldText must match uniquely).",
		"Keep oldText minimal (typically 2-5 lines).",
		"When modifying multiple separate locations in a file, provide multiple updates in updates[] in a single edit call.",
		"If edit reports an oldText mismatch, read the latest file and retry a smaller exact unique block. Never recover by overwriting the existing file with write.",
	}
}

var editToolDef = domaintool.NewFunctionTool(
	"edit",
	"Edit a file using exact text replacement blocks. Matches unique blocks against current file contents.",
	map[string]domaintool.SchemaProp{
		"path": domaintool.StringProp("Path to the file to edit. Always specify path first."),
		"updates": {
			Type:        "array",
			Description: "One or more targeted replacements.",
			Items: &domaintool.SchemaProp{
				Type: "object",
				Properties: map[string]domaintool.SchemaProp{
					"oldText": domaintool.StringProp("Exact unique current text copied from latest read (typically 2-5 lines)."),
					"newText": domaintool.StringProp("The replacement text for oldText."),
				},
				Required: []string{"oldText", "newText"},
			},
		},
	},
	"path", "updates",
)

func (t *editTool) Definition() domaintool.Tool {
	return editToolDef
}

func (t *editTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Path                     string        `json:"path"`
		File                     string        `json:"file"`
		FilePath                 string        `json:"file_path"`
		FilePathCamel            string        `json:"filePath"`
		TargetFile               string        `json:"target_file"`
		TargetFileCamel          string        `json:"targetFile"`
		TargetFilePascal         string        `json:"TargetFile"`
		FileName                 string        `json:"file_name"`
		FileNameCamel            string        `json:"fileName"`
		Filename                 string        `json:"filename"`
		Target                   string        `json:"target"`
		AbsolutePath             string        `json:"AbsolutePath"`
		AbsolutePathSnake        string        `json:"absolute_path"`
		Updates                  []ReplaceEdit `json:"updates"`
		Edits                    []ReplaceEdit `json:"edits"`
		Replacements             []ReplaceEdit `json:"replacements"`
		OldText                  string        `json:"oldText,omitempty"`
		NewText                  string        `json:"newText,omitempty"`
		OldTextSnake             string        `json:"old_text,omitempty"`
		NewTextSnake             string        `json:"new_text,omitempty"`
		OldString                string        `json:"old_string,omitempty"`
		NewString                string        `json:"new_string,omitempty"`
		TargetContent            string        `json:"targetContent,omitempty"`
		TargetContentSnake       string        `json:"target_content,omitempty"`
		TargetContentPascal      string        `json:"TargetContent,omitempty"`
		ReplacementContent       string        `json:"replacementContent,omitempty"`
		ReplacementContentSnake  string        `json:"replacement_content,omitempty"`
		ReplacementContentPascal string        `json:"ReplacementContent,omitempty"`
		Search                   string        `json:"search,omitempty"`
		Replace                  string        `json:"replace,omitempty"`
		Old                      string        `json:"old,omitempty"`
		New                      string        `json:"new,omitempty"`
		OldStr                   string        `json:"old_str,omitempty"`
		NewStr                   string        `json:"new_str,omitempty"`
	}
	unmarshalErr := json.Unmarshal([]byte(arguments), &args)

	edits := args.Updates
	if len(edits) == 0 && len(args.Edits) > 0 {
		edits = args.Edits
	} else if len(edits) == 0 && len(args.Replacements) > 0 {
		edits = args.Replacements
	}

	topEdit := ReplaceEdit{
		OldText:                  args.OldText,
		NewText:                  args.NewText,
		OldTextSnake:             args.OldTextSnake,
		NewTextSnake:             args.NewTextSnake,
		OldString:                args.OldString,
		NewString:                args.NewString,
		TargetContent:            args.TargetContent,
		TargetContentSnake:       args.TargetContentSnake,
		TargetContentPascal:      args.TargetContentPascal,
		ReplacementContent:       args.ReplacementContent,
		ReplacementContentSnake:  args.ReplacementContentSnake,
		ReplacementContentPascal: args.ReplacementContentPascal,
		Search:                   args.Search,
		Replace:                  args.Replace,
		Old:                      args.Old,
		New:                      args.New,
		OldStr:                   args.OldStr,
		NewStr:                   args.NewStr,
	}
	if topOld := topEdit.EffectiveOldText(); topOld != "" {
		edits = append(edits, ReplaceEdit{OldText: topOld, NewText: topEdit.EffectiveNewText()})
	}

	if len(edits) == 0 {
		edits = extractEdits(arguments)
	}

	var editCandidatePaths []string
	for _, e := range edits {
		for _, cp := range []string{e.Path, e.File, e.FilePath, e.TargetFile} {
			if strings.TrimSpace(cp) != "" {
				editCandidatePaths = append(editCandidatePaths, cp)
			}
		}
	}

	allCandidatePaths := append([]string{
		args.Path, args.FilePath, args.FilePathCamel,
		args.TargetFile, args.TargetFileCamel, args.TargetFilePascal,
		args.FileName, args.FileNameCamel, args.Filename,
		args.File, args.Target, args.AbsolutePath, args.AbsolutePathSnake,
	}, editCandidatePaths...)

	targetPath := extractFilePath(arguments, allCandidatePaths...)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	args.Path = targetPath

	if len(edits) == 0 {
		if unmarshalErr != nil {
			return "", fmt.Errorf("invalid arguments: %w", unmarshalErr)
		}
		return "", fmt.Errorf("no edits specified to apply")
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	unlock := lockPath(safePath)
	defer unlock()

	if fo, ok := ctx.(domaintool.FileObserver); ok {
		if err := fo.CheckMutationAllowed(safePath, true); err != nil {
			return "", err
		}
	}

	for i := range edits {
		edit := &edits[i]
		if effOld := edit.EffectiveOldText(); effOld != "" {
			edit.OldText = effOld
		}
		if effNew := edit.EffectiveNewText(); effNew != "" {
			edit.NewText = effNew
		}
		if placeholders := DetectOmissionPlaceholders(edit.NewText); len(placeholders) > 0 {
			return "", fmt.Errorf("refusing to edit file: detected code omission placeholder(s) in replacement text: %q. Please provide the complete new code replacement block without shorthand placeholders or comments like '// ... rest of code'.", placeholders)
		}
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	hasBOM, rawStr := splitBOM(data)
	originalEnding := detectLineEnding(rawStr)
	content := strings.ReplaceAll(rawStr, "\r\n", "\n")
	initialContent := content

	var diffBuilder strings.Builder
	contentChanged := false

	// Strategy 1: Parallel non-overlapping resolution against initialContent for multi-edit batches.
	if len(edits) > 1 {
		matches := make([]*textMatch, len(edits))
		allMatched := true
		for i := range edits {
			m, err := locateEditMatch(initialContent, edits[i].OldText, edits[i].NewText)
			if err != nil || m == nil {
				allMatched = false
				break
			}
			matches[i] = m
		}

		if allMatched {
			hasOverlap := false
			for a := 0; a < len(matches); a++ {
				for b := a + 1; b < len(matches); b++ {
					if matches[a].startByte < matches[b].endByte && matches[b].startByte < matches[a].endByte {
						hasOverlap = true
						break
					}
				}
				if hasOverlap {
					break
				}
			}

			if !hasOverlap {
				type orderedMatch struct {
					idx int
					m   *textMatch
				}
				order := make([]orderedMatch, len(matches))
				for i, m := range matches {
					order[i] = orderedMatch{idx: i, m: m}
				}
				sort.Slice(order, func(i, j int) bool {
					return order[i].m.startByte > order[j].m.startByte
				})

				for _, om := range order {
					m := om.m
					content = content[:m.startByte] + m.newText + content[m.endByte:]
					contentChanged = true
				}
				goto writeBack
			}
		}
	}

	// Strategy 2: Sequential execution for single edits or chained edits
	for i := range edits {
		edit := &edits[i]
		m, err := locateEditMatch(content, edit.OldText, edit.NewText)
		if err != nil {
			return "", fmt.Errorf("edit[%d]: %w", i, err)
		}
		if m == nil {
			if replacementAlreadyApplied(content, edit.NewText) {
				diffBuilder.WriteString(fmt.Sprintf("edit[%d]: requested replacement already present; file unchanged\n", i))
				continue
			}
			closestLine := findClosestLineMatch(content, edit.OldText)
			if closestLine > 0 {
				snippet := renderLineSnippet(strings.Split(content, "\n"), closestLine, 3)
				return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nDiagnostic Hint: A matching block was detected around line %d:\n---\n%s\n---\nTip: Notice the exact indentation/whitespace above. You can copy the exact block into 'oldText' without needing to call 'read'.", i, args.Path, closestLine, snippet)
			}
			return "", fmt.Errorf("edit[%d]: oldText block was not found in file %s.\nRecommendation: Read the file again using 'read' to verify its current contents, copy a small unique block exactly as it exists now, and retry without reusing an earlier snapshot. Do not recover by overwriting the existing file with write.", i, args.Path)
		}

		updatedContent := content[:m.startByte] + m.newText + content[m.endByte:]
		if updatedContent != content {
			contentChanged = true
		}
		content = updatedContent
	}

writeBack:

	if contentChanged {
		diffStr := generateDisplayDiff(initialContent, content, 3)
		diffBuilder.WriteString(diffStr)
		finalStr := content
		if originalEnding == "\r\n" {
			finalStr = strings.ReplaceAll(finalStr, "\n", "\r\n")
		}
		var finalBytes []byte
		if hasBOM {
			finalBytes = append([]byte{0xef, 0xbb, 0xbf}, []byte(finalStr)...)
		} else {
			finalBytes = []byte(finalStr)
		}
		useAtomic := true
		if cp, ok := ctx.(interface{ IsAtomicWritesEnabled() bool }); ok {
			useAtomic = cp.IsAtomicWritesEnabled()
		}
		if useAtomic {
			err = atomicWriteFile(safePath, finalBytes, 0644)
		} else {
			err = os.WriteFile(safePath, finalBytes, 0644)
		}
		if err != nil {
			return "", fmt.Errorf("failed to write modified content back: %w", err)
		}
		if fo, ok := ctx.(domaintool.FileObserver); ok {
			fo.RecordMutation(safePath, finalBytes)
		}
		ctx.ReloadSkills()
	}

	return diffBuilder.String(), nil
}
