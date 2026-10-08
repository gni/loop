package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type ReplaceEdit struct {
	OldText                  string `json:"oldText"`
	NewText                  string `json:"newText"`
	OldTextSnake             string `json:"old_text"`
	NewTextSnake             string `json:"new_text"`
	OldString                string `json:"old_string"`
	NewString                string `json:"new_string"`
	TargetContent            string `json:"targetContent"`
	TargetContentSnake       string `json:"target_content"`
	TargetContentPascal      string `json:"TargetContent"`
	ReplacementContent       string `json:"replacementContent"`
	ReplacementContentSnake  string `json:"replacement_content"`
	ReplacementContentPascal string `json:"ReplacementContent"`
	Search                   string `json:"search"`
	Replace                  string `json:"replace"`
	Old                      string `json:"old"`
	New                      string `json:"new"`
	OldStr                   string `json:"old_str"`
	NewStr                   string `json:"new_str"`
	Path                     string `json:"path"`
	File                     string `json:"file"`
	FilePath                 string `json:"file_path"`
	TargetFile               string `json:"target_file"`
}

func (e ReplaceEdit) EffectiveOldText() string {
	for _, val := range []string{
		e.OldText, e.OldTextSnake, e.OldString,
		e.TargetContent, e.TargetContentSnake, e.TargetContentPascal,
		e.Search, e.Old, e.OldStr,
	} {
		if val != "" {
			return val
		}
	}
	return ""
}

func (e ReplaceEdit) EffectiveNewText() string {
	for _, val := range []string{
		e.NewText, e.NewTextSnake, e.NewString,
		e.ReplacementContent, e.ReplacementContentSnake, e.ReplacementContentPascal,
		e.Replace, e.New, e.NewStr,
	} {
		if val != "" {
			return val
		}
	}
	return ""
}

var pathKeyRegex = regexp.MustCompile(`"(?i)(?:path|filePath|file_path|targetFile|target_file|TargetFile|fileName|file_name|filename|file|target|AbsolutePath|absolute_path|write_path|writePath)"\s*:\s*"([^"]+)"`)

func extractFilePath(rawJSON string, candidatePaths ...string) string {
	for _, p := range candidatePaths {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			return trimmed
		}
	}

	trimmedJSON := strings.TrimSpace(rawJSON)
	if trimmedJSON == "" {
		return ""
	}

	var genericSlice []interface{}
	if err := json.Unmarshal([]byte(trimmedJSON), &genericSlice); err == nil && len(genericSlice) > 0 {
		if firstItem, ok := genericSlice[0].(map[string]interface{}); ok {
			if p := findPathInGenericMap(firstItem); p != "" {
				return p
			}
		}
	}

	var generic map[string]interface{}
	if err := json.Unmarshal([]byte(trimmedJSON), &generic); err == nil && generic != nil {
		if p := findPathInGenericMap(generic); p != "" {
			return p
		}
	}

	if matches := pathKeyRegex.FindStringSubmatch(trimmedJSON); len(matches) > 1 {
		res := strings.TrimSpace(matches[1])
		if res != "" {
			return res
		}
	}

	if !strings.HasPrefix(trimmedJSON, "{") && !strings.HasPrefix(trimmedJSON, "[") {
		cleaned := strings.Trim(trimmedJSON, "\"'` \t\r\n")
		if cleaned != "" && !strings.ContainsAny(cleaned, "\n\r{}[]") {
			return cleaned
		}
	}

	return ""
}

func findPathInGenericMap(m map[string]interface{}) string {
	keys := []string{
		"path", "Path",
		"file_path", "filePath", "FilePath",
		"target_file", "targetFile", "TargetFile",
		"filename", "fileName", "FileName", "file_name",
		"file", "File",
		"target", "Target",
		"AbsolutePath", "absolute_path", "absolutepath",
		"write_path", "writePath", "WritePath",
		"dest", "destination", "dest_file",
		"location", "uri", "URI",
	}

	for _, k := range keys {
		if val, ok := m[k]; ok {
			if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}

	wrappers := []string{"input", "parameters", "arguments", "edit", "params", "tool_input"}
	for _, w := range wrappers {
		if val, ok := m[w]; ok {
			if subMap, ok := val.(map[string]interface{}); ok {
				if p := findPathInGenericMap(subMap); p != "" {
					return p
				}
			}
		}
	}

	listKeys := []string{"updates", "edits", "replacements"}
	for _, lk := range listKeys {
		if val, ok := m[lk]; ok {
			if slice, ok := val.([]interface{}); ok && len(slice) > 0 {
				if firstItem, ok := slice[0].(map[string]interface{}); ok {
					if p := findPathInGenericMap(firstItem); p != "" {
						return p
					}
				}
			} else if subMap, ok := val.(map[string]interface{}); ok {
				if p := findPathInGenericMap(subMap); p != "" {
					return p
				}
			}
		}
	}

	return ""
}

func extractEditsFromGenericMap(m map[string]interface{}) []ReplaceEdit {
	var results []ReplaceEdit

	listKeys := []string{"updates", "edits", "replacements"}
	for _, lk := range listKeys {
		if val, ok := m[lk]; ok {
			if slice, ok := val.([]interface{}); ok {
				for _, item := range slice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						oldT := findStringInMap(itemMap, "oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr")
						newT := findStringInMap(itemMap, "newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr")
						if oldT != "" {
							results = append(results, ReplaceEdit{OldText: oldT, NewText: newT})
						}
					}
				}
			} else if itemMap, ok := val.(map[string]interface{}); ok {
				oldT := findStringInMap(itemMap, "oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr")
				newT := findStringInMap(itemMap, "newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr")
				if oldT != "" {
					results = append(results, ReplaceEdit{OldText: oldT, NewText: newT})
				}
			}
		}
	}

	if len(results) > 0 {
		return results
	}

	for _, w := range []string{"input", "parameters", "arguments", "edit"} {
		if val, ok := m[w]; ok {
			if subMap, ok := val.(map[string]interface{}); ok {
				if subEdits := extractEditsFromGenericMap(subMap); len(subEdits) > 0 {
					return subEdits
				}
			}
		}
	}

	oldT := findStringInMap(m, "oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr")
	newT := findStringInMap(m, "newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr")
	if oldT != "" {
		results = append(results, ReplaceEdit{OldText: oldT, NewText: newT})
	}

	return results
}

func extractEdits(rawJSON string, candidateEdits ...[]ReplaceEdit) []ReplaceEdit {
	var merged []ReplaceEdit
	for _, group := range candidateEdits {
		for _, e := range group {
			if e.EffectiveOldText() != "" {
				merged = append(merged, ReplaceEdit{
					OldText: e.EffectiveOldText(),
					NewText: e.EffectiveNewText(),
				})
			}
		}
	}
	if len(merged) > 0 {
		return merged
	}

	trimmedJSON := strings.TrimSpace(rawJSON)
	if trimmedJSON == "" {
		return nil
	}

	var genericSlice []interface{}
	if err := json.Unmarshal([]byte(trimmedJSON), &genericSlice); err == nil && len(genericSlice) > 0 {
		for _, item := range genericSlice {
			if itemMap, ok := item.(map[string]interface{}); ok {
				oldT := findStringInMap(itemMap, "oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr")
				newT := findStringInMap(itemMap, "newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr")
				if oldT != "" {
					merged = append(merged, ReplaceEdit{OldText: oldT, NewText: newT})
				}
			}
		}
		if len(merged) > 0 {
			return merged
		}
	}

	var generic map[string]interface{}
	if err := json.Unmarshal([]byte(trimmedJSON), &generic); err == nil && generic != nil {
		return extractEditsFromGenericMap(generic)
	}

	return nil
}

func extractFileContent(rawJSON string, candidateContents ...string) (string, bool) {
	for _, c := range candidateContents {
		if c != "" {
			return c, true
		}
	}
	var generic map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &generic); err == nil && generic != nil {
		return findContentInGenericMap(generic)
	}
	return "", false
}

func findContentInGenericMap(m map[string]interface{}) (string, bool) {
	keys := []string{
		"content", "Content",
		"code_content", "codeContent", "CodeContent",
		"write_content", "writeContent",
		"file_content", "fileContent",
		"text", "Text",
		"body", "Body",
		"data", "Data",
		"contents", "Contents",
	}
	for _, k := range keys {
		if val, ok := m[k]; ok {
			if s, ok := val.(string); ok {
				return s, true
			}
		}
	}
	wrappers := []string{"input", "parameters", "arguments", "write", "params", "tool_input"}
	for _, w := range wrappers {
		if val, ok := m[w]; ok {
			if subMap, ok := val.(map[string]interface{}); ok {
				if c, ok := findContentInGenericMap(subMap); ok {
					return c, true
				}
			}
		}
	}
	return "", false
}

func findStringInMap(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			if s, ok := val.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

type readTool struct{}

func NewReadTool() ToolExecutor {
	return &readTool{}
}

func (t *readTool) Name() string { return "read" }

func (t *readTool) PromptSnippet() string {
	return "Read file contents"
}

func (t *readTool) PromptGuidelines() []string {
	return []string{
		"Use 'read' to examine files instead of cat or sed in bash. Do not call 'read' on directory paths; use 'list' to inspect directory trees.",
		"In tool call arguments, always output 'path' first.",
	}
}

func (t *readTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "read",
			Description: "Read file contents. Supports text files. Specify 'path' first before offset or limit.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to a specific file to read (relative or absolute). Specify this parameter first. Do not pass directory paths; use 'list' to inspect directory trees.",
					},
					"offset": {
						Type:        "number",
						Description: "Line number to start reading from (1-indexed). Optional",
					},
					"limit": {
						Type:        "number",
						Description: "Maximum number of lines to read. Optional",
					},
				},
				Required: []string{"path"},
			},
		},
	}
}

func (t *readTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path              string  `json:"path"`
		File              string  `json:"file"`
		FilePath          string  `json:"file_path"`
		FilePathCamel     string  `json:"filePath"`
		TargetFile        string  `json:"target_file"`
		TargetFileCamel   string  `json:"targetFile"`
		TargetFilePascal  string  `json:"TargetFile"`
		FileName          string  `json:"file_name"`
		FileNameCamel     string  `json:"fileName"`
		Filename          string  `json:"filename"`
		Target            string  `json:"target"`
		AbsolutePath      string  `json:"AbsolutePath"`
		AbsolutePathSnake string  `json:"absolute_path"`
		Offset            float64 `json:"offset"`
		Limit             float64 `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		var rawPath string
		if errStr := json.Unmarshal([]byte(arguments), &rawPath); errStr == nil && strings.TrimSpace(rawPath) != "" {
			args.Path = rawPath
		} else {
			trimmed := strings.TrimSpace(arguments)
			if !strings.HasPrefix(trimmed, "{") && trimmed != "" {
				args.Path = trimmed
			}
		}
	}

	targetPath := extractFilePath(arguments,
		args.Path, args.FilePath, args.FilePathCamel,
		args.TargetFile, args.TargetFileCamel, args.TargetFilePascal,
		args.FileName, args.FileNameCamel, args.Filename,
		args.File, args.Target, args.AbsolutePath, args.AbsolutePathSnake,
	)
	if targetPath == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	args.Path = targetPath

	if args.Offset == 0 || args.Limit == 0 {
		var generic map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &generic); err == nil && generic != nil {
			for _, w := range []string{"input", "parameters", "arguments", "read", "params", "tool_input"} {
				if sub, ok := generic[w].(map[string]interface{}); ok {
					if args.Offset == 0 {
						if off, ok := sub["offset"].(float64); ok {
							args.Offset = off
						}
					}
					if args.Limit == 0 {
						if lim, ok := sub["limit"].(float64); ok {
							args.Limit = lim
						}
					}
				}
			}
		}
	}

	safePath, err := ctx.SafePath(args.Path)
	if err != nil {
		return "", err
	}

	if hasIgnoredComponent(args.Path) {
		return "", fmt.Errorf("cannot read: path '%s' is inside a dependency or ignored folder (venv, node_modules, etc.)", args.Path)
	}

	unlock := lockPath(safePath)
	defer unlock()

	var entryHeader string
	info, err := os.Stat(safePath)
	if err != nil {
		found := false
		for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go", ".json", ".md", ".yaml", ".yml"} {
			candidate := safePath + ext
			if cInfo, cErr := os.Stat(candidate); cErr == nil && !cInfo.IsDir() {
				safePath = candidate
				info = cInfo
				found = true
				relPath, _ := filepath.Rel(ctx.GetWorkspaceRoot(), candidate)
				if relPath == "" {
					relPath = args.Path + ext
				}
				entryHeader = fmt.Sprintf("[Notice: Resolved '%s' to '%s']\n\n", args.Path, relPath)
				break
			}
		}
		if !found {
			for _, ext := range []string{".py", ".ts", ".js", ".tsx", ".jsx", ".go"} {
				if strings.HasSuffix(safePath, ext) {
					trimmed := strings.TrimSuffix(safePath, ext)
					if dInfo, dErr := os.Stat(trimmed); dErr == nil && dInfo.IsDir() {
						safePath = trimmed
						info = dInfo
						found = true
						break
					}
				}
			}
		}
		if !found {
			return "", fmt.Errorf("failed to read file info: %w", err)
		}
	}
	if info.IsDir() {
		foundEntry := false
		for _, entryName := range []string{"__init__.py", "index.ts", "index.js", "index.tsx", "index.jsx", "main.go"} {
			entryPath := filepath.Join(safePath, entryName)
			if entryInfo, err := os.Stat(entryPath); err == nil && !entryInfo.IsDir() && entryInfo.Size() > 0 {
				safePath = entryPath
				info = entryInfo
				foundEntry = true
				relEntry, _ := filepath.Rel(ctx.GetWorkspaceRoot(), entryPath)
				if relEntry == "" {
					relEntry = filepath.Join(args.Path, entryName)
				}
				entryHeader = fmt.Sprintf("[Notice: '%s' is a directory. Automatically reading package entrypoint '%s':]\n\n", args.Path, relEntry)
				break
			}
		}

		if !foundEntry {
			tree, err := ListDirectoryTree(safePath, ctx.GetWorkspaceRoot(), 2, 150)
			if err != nil {
				return "", fmt.Errorf("path '%s' is a directory and failed to list contents: %w", args.Path, err)
			}
			return fmt.Sprintf("[Path '%s' is a directory. Use 'list' to view directories, or call 'read' with a specific file path to view its content:]\n\n%s", args.Path, tree), nil
		}
	}
	if info.Size() > 500*1024 { // 500KB limit
		return "", fmt.Errorf("file size (%d bytes) is too large; maximum allowed size is 500KB", info.Size())
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if fo, ok := ctx.(FileObserver); ok {
		fo.RecordRead(safePath, data)
	}

	// Check if file is binary
	if isBinary(data) {
		return "", fmt.Errorf("cannot read binary file; the read tool only supports text files")
	}

	_, textWithoutBOM := splitBOM(data)
	contentStr := SanitizeUTF8([]byte(textWithoutBOM))
	if len(data) == 0 || strings.TrimSpace(contentStr) == "" {
		return entryHeader + "(empty file)", nil
	}

	lines := strings.Split(contentStr, "\n")
	offset := int(args.Offset)
	if offset <= 0 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}

	limit := int(args.Limit)
	if limit <= 0 {
		limit = 1000 // default to 1000 lines so normal files are read completely in one call
	} else if limit > 2000 {
		limit = 2000 // cap maximum lines per read to 2000
	}

	end := offset + limit - 1
	truncated := false
	if end > len(lines) {
		end = len(lines)
	} else if end < len(lines) {
		truncated = true
	}

	var resultLines []string
	for i := offset - 1; i < end; i++ {
		line := lines[i]
		if len(line) > 1000 {
			line = line[:1000] + " ... [line truncated: line is too long] ..."
		}
		resultLines = append(resultLines, line)
	}

	result := strings.Join(resultLines, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[Showing lines %d to %d of %d. Use read with offset=%d to view more]", offset, end, len(lines), end+1)
	}
	return entryHeader + result, nil
}

type writeTool struct{}

func NewWriteTool() ToolExecutor {
	return &writeTool{}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) PromptSnippet() string {
	return "Create or overwrite complete files"
}

func (t *writeTool) PromptGuidelines() []string {
	return []string{
		"Use 'write' only for new files or complete rewrites. Never use after an edit mismatch.",
		"Specify the target file in 'path' and the complete file contents in 'content'.",
		"In tool call arguments, always output 'path' before 'content' to enable real-time streaming preview and syntax highlighting.",
	}
}

func (t *writeTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "write",
			Description: "Create a new file or completely overwrite an existing file. Specify 'path' first before 'content'. Automatically creates parent directories.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the target file. Specify 'path' first before 'content'.",
					},
					"content": {
						Type:        "string",
						Description: "Complete content to write into the file.",
					},
				},
				Required: []string{"path", "content"},
			},
		},
	}
}

func (t *writeTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Path               string  `json:"path"`
		File               string  `json:"file"`
		FilePath           string  `json:"file_path"`
		FilePathCamel      string  `json:"filePath"`
		Target             string  `json:"target"`
		TargetFile         string  `json:"target_file"`
		TargetFileCamel    string  `json:"targetFile"`
		TargetFilePascal   string  `json:"TargetFile"`
		FileName           string  `json:"file_name"`
		FileNameCamel      string  `json:"fileName"`
		Filename           string  `json:"filename"`
		WritePath          string  `json:"write_path"`
		WritePathCamel     string  `json:"writePath"`
		AbsolutePath       string  `json:"AbsolutePath"`
		AbsolutePathSnake  string  `json:"absolute_path"`
		Content            *string `json:"content"`
		CodeContent        *string `json:"code_content"`
		CodeContentCamel   *string `json:"codeContent"`
		CodeContentPascal  *string `json:"CodeContent"`
		WriteContent       *string `json:"write_content"`
		Text               *string `json:"text"`
		Body               *string `json:"body"`
		Data               *string `json:"data"`
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

	if fo, ok := ctx.(FileObserver); ok {
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
	if fo, ok := ctx.(FileObserver); ok {
		fo.RecordMutation(safePath, []byte(content))
	}
	ctx.ReloadSkills()
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), args.Path), nil
}

type editTool struct{}

func NewEditTool() ToolExecutor {
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

func (t *editTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "edit",
			Description: "Edit a file using exact text replacement blocks. Matches unique blocks against current file contents.",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {
						Type:        "string",
						Description: "Path to the file to edit. Always specify path first.",
					},
					"updates": {
						Type:        "array",
						Description: "One or more targeted replacements.",
						Items: &SchemaProp{
							Type: "object",
							Properties: map[string]SchemaProp{
								"oldText": {
									Type:        "string",
									Description: "Exact unique current text copied from latest read (typically 2-5 lines).",
								},
								"newText": {
									Type:        "string",
									Description: "The replacement text for oldText.",
								},
							},
							Required: []string{"oldText", "newText"},
						},
					},
				},
				Required: []string{"path", "updates"},
			},
		},
	}
}

func (t *editTool) Execute(ctx AgentContext, arguments string) (string, error) {
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

	if fo, ok := ctx.(FileObserver); ok {
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
	// Models typically construct all edits in a turn against the initial file content.
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
		if fo, ok := ctx.(FileObserver); ok {
			fo.RecordMutation(safePath, finalBytes)
		}
		ctx.ReloadSkills()
	}

	return diffBuilder.String(), nil
}

// atomicWriteFile writes data to a temporary file in the destination's directory,
// flushes it to disk via Sync, and atomically renames it to filename.
// This prevents corrupted or empty files during crashes, timeouts, or interruptions.
func atomicWriteFile(filename string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(dir, fmt.Sprintf(".tmp-%s-*", filepath.Base(filename)))
	if err != nil {
		return os.WriteFile(filename, data, perm)
	}
	tmpName := tmpFile.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, perm)

	if err := os.Rename(tmpName, filename); err != nil {
		return os.WriteFile(filename, data, perm)
	}
	tmpName = ""
	return nil
}

func isBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

func SanitizeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}

	var r []rune
	for len(data) > 0 {
		run, size := utf8.DecodeRune(data)
		if run == utf8.RuneError && size == 1 {
			r = append(r, ' ')
		} else {
			r = append(r, run)
		}
		data = data[size:]
	}
	return string(r)
}

var omissionRegexes = []*regexp.Regexp{
	// Matches lines containing "rest of code", "rest of method(s)", "unchanged code", etc.
	regexp.MustCompile(`(?i)(?:rest of|unchanged|same as|original|existing)\s+(?:code|methods?|functions?|class(?:es)?|files?|implementations?)\s*\.{3,}`),
	// Matches lines with just comments and dots: e.g. // ... or # ... or /* ... */
	regexp.MustCompile(`(?i)^\s*(?://|#|/\*)\s*\.{3,}\s*(?:\*/)?\s*$`),
	// Matches brackets with dots: (...)
	regexp.MustCompile(`^\s*\(\s*\.{3,}\s*\)\s*$`),
	// Matches TODO comments that suggest omission: e.g. // TODO: implement the rest or // TODO ...
	regexp.MustCompile(`(?i)(?://|#|/\*)\s*todo\s*[\:\-\s]*\.*(?:\s*(?:implement|add|write)\s+(?:the\s+)?(rest|remaining|code|methods?))?\s*\.{3,}`),
}

// DetectOmissionPlaceholders searches for code omission comments like '// ... rest of code'.
func DetectOmissionPlaceholders(text string) []string {
	var matches []string
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, rx := range omissionRegexes {
			if rx.MatchString(trimmed) {
				matches = append(matches, line)
				break
			}
		}
	}
	return matches
}

type refMutex struct {
	mu   sync.Mutex
	refs int
}

var (
	fileLocks   = make(map[string]*refMutex)
	fileLocksMu sync.Mutex
)

func lockPath(path string) func() {
	fileLocksMu.Lock()
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	entry, exists := fileLocks[absPath]
	if !exists {
		entry = &refMutex{}
		fileLocks[absPath] = entry
	}
	entry.refs++
	fileLocksMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		fileLocksMu.Lock()
		entry.refs--
		if entry.refs <= 0 {
			delete(fileLocks, absPath)
		}
		fileLocksMu.Unlock()
	}
}

func splitBOM(data []byte) (bool, string) {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return true, string(data[3:])
	}
	return false, string(data)
}

func detectLineEnding(content string) string {
	crlf := strings.Index(content, "\r\n")
	lf := strings.Index(content, "\n")
	if lf == -1 {
		return "\n"
	}
	if crlf == -1 {
		return "\n"
	}
	if crlf < lf {
		return "\r\n"
	}
	return "\n"
}

func normalizeForFuzzyMatch(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\u2018', '\u2019', '\u201a', '\u201b': // Smart single quotes
			b.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f': // Smart double quotes
			b.WriteRune('"')
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212': // Dashes & minus
			b.WriteRune('-')
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000': // Unicode spaces
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(normalizeForFuzzyMatch(s)), " ")
}

func replacementAlreadyApplied(content, newText string) bool {
	trimmed := strings.TrimSpace(newText)
	if trimmed == "" {
		return false
	}
	if len(trimmed) < 16 && !strings.Contains(newText, "\n") {
		return false
	}
	return strings.Count(content, newText) == 1
}

func hasIgnoredComponent(path string) bool {
	path = filepath.Clean(path)
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		low := strings.ToLower(part)
		if low == "node_modules" || low == "venv" || low == ".venv" || low == ".git" || low == "__pycache__" {
			return true
		}
	}
	return false
}

type textMatch struct {
	startByte   int
	endByte     int
	matchedText string
	newText     string
}

type wordToken struct {
	text      string
	norm      string
	startByte int
	endByte   int
}

func extractWordTokens(s string) []wordToken {
	var tokens []wordToken
	inWord := false
	start := 0
	for i, r := range s {
		if unicode.IsSpace(r) {
			if inWord {
				word := s[start:i]
				tokens = append(tokens, wordToken{
					text:      word,
					norm:      strings.ToLower(normalizeForFuzzyMatch(word)),
					startByte: start,
					endByte:   i,
				})
				inWord = false
			}
		} else {
			if !inWord {
				start = i
				inWord = true
			}
		}
	}
	if inWord {
		word := s[start:]
		tokens = append(tokens, wordToken{
			text:      word,
			norm:      strings.ToLower(normalizeForFuzzyMatch(word)),
			startByte: start,
			endByte:   len(s),
		})
	}
	return tokens
}

func normalizeBullet(s string) string {
	if s == "*" || s == "-" || s == "+" || s == "•" {
		return "-"
	}
	return s
}

func matchWordTokens(cw, ow wordToken) bool {
	if cw.norm == ow.norm {
		return true
	}
	if normalizeBullet(cw.norm) == normalizeBullet(ow.norm) {
		return true
	}
	cTrim := strings.TrimRight(cw.norm, ".,;:!?")
	oTrim := strings.TrimRight(ow.norm, ".,;:!?")
	if cTrim == oTrim && cTrim != "" {
		return true
	}
	return false
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

func locateEditMatch(content, oldText, newText string) (*textMatch, error) {
	if oldText == "" {
		return nil, nil
	}

	// Tier 1: Exact substring match
	exactCount := strings.Count(content, oldText)
	if exactCount == 1 {
		start := strings.Index(content, oldText)
		return &textMatch{
			startByte:   start,
			endByte:     start + len(oldText),
			matchedText: oldText,
			newText:     newText,
		}, nil
	}
	if exactCount > 1 {
		return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", exactCount)
	}

	// Tier 2: Unicode and quote/dash/space normalization
	normContent := normalizeForFuzzyMatch(content)
	normOld := normalizeForFuzzyMatch(oldText)
	if normOld != oldText || normContent != content {
		normCount := strings.Count(normContent, normOld)
		if normCount == 1 {
			normIdx := strings.Index(normContent, normOld)
			normRunes := []rune(normContent)
			contentRunes := []rune(content)
			if len(normRunes) == len(contentRunes) {
				runeStart := len([]rune(normContent[:normIdx]))
				runeLen := len([]rune(normOld))
				if runeStart+runeLen <= len(contentRunes) {
					matchedRunes := contentRunes[runeStart : runeStart+runeLen]
					matchedText := string(matchedRunes)
					startByte := len(string(contentRunes[:runeStart]))
					endByte := startByte + len(matchedText)
					if endByte <= len(content) && content[startByte:endByte] == matchedText {
						return &textMatch{
							startByte:   startByte,
							endByte:     endByte,
							matchedText: matchedText,
							newText:     newText,
						}, nil
					}
				}
			}
		} else if normCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", normCount)
		}
	}

	// Tier 3: Line-by-line whitespace-trimmed and normalized matching
	fileLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")

	lineStarts := make([]int, len(fileLines))
	lineEnds := make([]int, len(fileLines))
	offset := 0
	for idx, fl := range fileLines {
		lineStarts[idx] = offset
		offset += len(fl)
		lineEnds[idx] = offset
		offset++ // for newline
	}

	cleanOldLines := make([]string, len(oldLines))
	for i, l := range oldLines {
		cleanOldLines[i] = strings.TrimSpace(l)
	}

	startIdx := 0
	for startIdx < len(cleanOldLines) && cleanOldLines[startIdx] == "" {
		startIdx++
	}
	endIdx := len(cleanOldLines)
	for endIdx > startIdx && cleanOldLines[endIdx-1] == "" {
		endIdx--
	}
	coreOldLines := cleanOldLines[startIdx:endIdx]

	if len(coreOldLines) > 0 {
		matchStart := -1
		matchEnd := -1
		matchesCount := 0

		for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
			matched := true
			for j := 0; j < len(coreOldLines); j++ {
				fileLineNorm := normalizeSpace(fileLines[fs+j])
				oldLineNorm := normalizeSpace(coreOldLines[j])
				if fileLineNorm != oldLineNorm {
					matched = false
					break
				}
			}
			if matched {
				matchStart = fs
				matchEnd = fs + len(coreOldLines)
				matchesCount++
			}
		}

		if matchesCount == 1 {
			actualStart := matchStart
			for actualStart > 0 && matchStart-actualStart < startIdx {
				if strings.TrimSpace(fileLines[actualStart-1]) == "" {
					actualStart--
				} else {
					break
				}
			}
			actualEnd := matchEnd
			for actualEnd < len(fileLines) && actualEnd-matchEnd < (len(cleanOldLines)-endIdx) {
				if strings.TrimSpace(fileLines[actualEnd]) == "" {
					actualEnd++
				} else {
					break
				}
			}
			startByte := lineStarts[actualStart]
			endByte := lineEnds[actualEnd-1]
			matchedText := content[startByte:endByte]

			adjustedNew := newText
			fileIndent := leadingWhitespace(fileLines[actualStart])
			oldIndent := leadingWhitespace(oldLines[0])
			if fileIndent != oldIndent && strings.HasPrefix(fileIndent, oldIndent) {
				indentDiff := fileIndent[len(oldIndent):]
				if indentDiff != "" {
					newLines := strings.Split(newText, "\n")
					allIndented := true
					for _, nl := range newLines {
						if nl != "" && !strings.HasPrefix(nl, fileIndent) {
							allIndented = false
							break
						}
					}
					if !allIndented {
						for k := range newLines {
							if newLines[k] != "" {
								newLines[k] = indentDiff + newLines[k]
							}
						}
						adjustedNew = strings.Join(newLines, "\n")
					}
				}
			}

			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     adjustedNew,
			}, nil
		}
		if matchesCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchesCount)
		}
	}

	// Tier 4: Word-stream sequence matching (Markdown soft-wraps, paragraphs, bullets)
	contentWords := extractWordTokens(content)
	oldWords := extractWordTokens(oldText)
	if len(oldWords) >= 2 || (len(oldWords) == 1 && len(oldWords[0].text) >= 6) {
		matchStart := -1
		matchCount := 0
		for i := 0; i <= len(contentWords)-len(oldWords); i++ {
			matched := true
			for j := 0; j < len(oldWords); j++ {
				if !matchWordTokens(contentWords[i+j], oldWords[j]) {
					matched = false
					break
				}
			}
			if matched {
				matchStart = i
				matchCount++
			}
		}

		if matchCount == 1 && matchStart >= 0 {
			startByte := contentWords[matchStart].startByte
			endByte := contentWords[matchStart+len(oldWords)-1].endByte

			if strings.HasPrefix(oldText, "\n") || leadingWhitespace(oldText) != "" {
				lineStart := strings.LastIndex(content[:startByte], "\n")
				if lineStart >= 0 {
					prefix := content[lineStart+1 : startByte]
					if strings.TrimSpace(prefix) == "" || normalizeBullet(strings.TrimSpace(prefix)) == "-" {
						startByte = lineStart + 1
					}
				} else if strings.TrimSpace(content[:startByte]) == "" {
					startByte = 0
				}
			}
			if strings.HasSuffix(oldText, "\n") {
				lineEnd := strings.Index(content[endByte:], "\n")
				if lineEnd >= 0 {
					endByte += lineEnd + 1
				}
			}

			matchedText := content[startByte:endByte]
			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     newText,
			}, nil
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchCount)
		}
	}

	// Tier 5: Single-line / Partial-line substring resilient match
	if len(oldLines) == 1 && strings.TrimSpace(oldText) != "" {
		trimmedOld := strings.TrimSpace(oldText)
		oldNorm := normalizeSpace(trimmedOld)
		oldLower := strings.ToLower(oldNorm)

		matchLine := -1
		matchPhraseStart := -1
		matchPhraseEnd := -1
		matchCount := 0

		for idx, fl := range fileLines {
			flNorm := normalizeSpace(fl)
			flLower := strings.ToLower(flNorm)
			if strings.Contains(flLower, oldLower) || strings.Contains(flNorm, oldNorm) {
				flOrigLower := strings.ToLower(fl)
				subIdx := strings.Index(flOrigLower, strings.ToLower(trimmedOld))
				if subIdx >= 0 {
					matchLine = idx
					matchPhraseStart = subIdx
					matchPhraseEnd = subIdx + len(trimmedOld)
					matchCount++
				}
			}
		}

		if matchCount == 1 && matchLine >= 0 {
			startByte := lineStarts[matchLine] + matchPhraseStart
			endByte := lineStarts[matchLine] + matchPhraseEnd
			if endByte <= len(content) {
				return &textMatch{
					startByte:   startByte,
					endByte:     endByte,
					matchedText: content[startByte:endByte],
					newText:     newText,
				}, nil
			}
		}
		if matchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", matchCount)
		}
	}

	// Tier 6: Multi-line sliding window fuzzy similarity match (Levenshtein / token similarity)
	if len(coreOldLines) >= 1 {
		bestMatchStart := -1
		bestMatchScore := 0.0
		bestMatchCount := 0

		for fs := 0; fs <= len(fileLines)-len(coreOldLines); fs++ {
			score := 0.0
			for j := 0; j < len(coreOldLines); j++ {
				fNorm := normalizeSpace(fileLines[fs+j])
				oNorm := normalizeSpace(coreOldLines[j])
				if fNorm == oNorm {
					score += 1.0
				} else if strings.Contains(fNorm, oNorm) || strings.Contains(oNorm, fNorm) {
					score += 0.8
				} else {
					fWords := strings.Fields(strings.ToLower(fNorm))
					oWords := strings.Fields(strings.ToLower(oNorm))
					if len(fWords) > 0 && len(oWords) > 0 {
						common := 0
						for _, ow := range oWords {
							for _, fw := range fWords {
								if ow == fw {
									common++
									break
								}
							}
						}
						wordScore := float64(common) / float64(len(oWords))
						if wordScore >= 0.5 {
							score += wordScore * 0.7
						}
					}
				}
			}
			normalizedScore := score / float64(len(coreOldLines))
			minThreshold := 0.75
			if len(coreOldLines) <= 2 {
				minThreshold = 0.80
			}
			if normalizedScore >= minThreshold {
				if normalizedScore > bestMatchScore {
					bestMatchScore = normalizedScore
					bestMatchStart = fs
					bestMatchCount = 1
				} else if normalizedScore == bestMatchScore {
					bestMatchCount++
				}
			}
		}

		if bestMatchCount == 1 && bestMatchStart >= 0 {
			actualStart := bestMatchStart
			actualEnd := bestMatchStart + len(coreOldLines)
			startByte := lineStarts[actualStart]
			endByte := lineEnds[actualEnd-1]
			matchedText := content[startByte:endByte]
			return &textMatch{
				startByte:   startByte,
				endByte:     endByte,
				matchedText: matchedText,
				newText:     newText,
			}, nil
		}
		if bestMatchCount > 1 {
			return nil, fmt.Errorf("oldText block is not unique; found %d occurrences in file", bestMatchCount)
		}
	}

	return nil, nil
}

func findClosestLineMatch(content, oldText string) int {
	fileLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")
	var coreLine string
	for _, l := range oldLines {
		t := strings.TrimSpace(l)
		if len(t) > 3 {
			coreLine = t
			break
		}
	}
	if coreLine == "" {
		return 0
	}
	coreNorm := normalizeSpace(coreLine)
	coreLower := strings.ToLower(coreNorm)

	// Pass 1: exact or normalized match
	for idx, fLine := range fileLines {
		if strings.Contains(fLine, coreLine) || normalizeSpace(fLine) == coreNorm {
			return idx + 1
		}
	}

	// Pass 2: case-insensitive match
	for idx, fLine := range fileLines {
		fNorm := strings.ToLower(normalizeSpace(fLine))
		if strings.Contains(fNorm, coreLower) {
			return idx + 1
		}
	}

	// Pass 3: highest word overlap
	coreWords := strings.Fields(coreLower)
	if len(coreWords) >= 2 {
		bestIdx := -1
		bestScore := 0
		for idx, fLine := range fileLines {
			fWords := strings.Fields(strings.ToLower(fLine))
			score := 0
			for _, cw := range coreWords {
				for _, fw := range fWords {
					if cw == fw || strings.Contains(fw, cw) {
						score++
						break
					}
				}
			}
			if score > bestScore && score >= len(coreWords)/2 && score >= 2 {
				bestScore = score
				bestIdx = idx
			}
		}
		if bestIdx >= 0 {
			return bestIdx + 1
		}
	}

	return 0
}

func renderLineSnippet(fileLines []string, centerLine int, radius int) string {
	start := centerLine - radius - 1
	if start < 0 {
		start = 0
	}
	end := centerLine + radius
	if end > len(fileLines) {
		end = len(fileLines)
	}
	var sb strings.Builder
	for i := start; i < end; i++ {
		marker := " "
		if i == centerLine-1 {
			marker = ">"
		}
		sb.WriteString(fmt.Sprintf("%s %4d | %s\n", marker, i+1, fileLines[i]))
	}
	return strings.TrimRight(sb.String(), "\n")
}

type diffOp int

const (
	diffEqual diffOp = iota
	diffInsert
	diffDelete
)

type diffPart struct {
	op    diffOp
	lines []string
}

func computeMyersDiff(a, b []string) []diffPart {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	if len(a) == 0 {
		return []diffPart{{op: diffInsert, lines: b}}
	}
	if len(b) == 0 {
		return []diffPart{{op: diffDelete, lines: a}}
	}

	prefixLen := 0
	for prefixLen < len(a) && prefixLen < len(b) && a[prefixLen] == b[prefixLen] {
		prefixLen++
	}

	suffixLen := 0
	for suffixLen < len(a)-prefixLen && suffixLen < len(b)-prefixLen && a[len(a)-1-suffixLen] == b[len(b)-1-suffixLen] {
		suffixLen++
	}

	var parts []diffPart
	if prefixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[:prefixLen]})
	}

	midA := a[prefixLen : len(a)-suffixLen]
	midB := b[prefixLen : len(b)-suffixLen]
	n := len(midA)
	m := len(midB)

	if n == 0 && m == 0 {
		// Nothing in middle
	} else if n == 0 {
		parts = append(parts, diffPart{op: diffInsert, lines: midB})
	} else if m == 0 {
		parts = append(parts, diffPart{op: diffDelete, lines: midA})
	} else if n+m > 2000 {
		parts = append(parts, diffPart{op: diffDelete, lines: midA})
		parts = append(parts, diffPart{op: diffInsert, lines: midB})
	} else {
		maxD := n + m
		offset := maxD
		v := make([]int, 2*maxD+1)
		trace := make([][]int, 0, maxD+1)

		dFound := -1
		for d := 0; d <= maxD; d++ {
			vCopy := make([]int, len(v))
			copy(vCopy, v)
			trace = append(trace, vCopy)

			for k := -d; k <= d; k += 2 {
				var x int
				if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
					x = v[k+1+offset]
				} else {
					x = v[k-1+offset] + 1
				}
				y := x - k
				for x < n && y < m && midA[x] == midB[y] {
					x++
					y++
				}
				v[k+offset] = x
				if x >= n && y >= m {
					dFound = d
					break
				}
			}
			if dFound != -1 {
				break
			}
		}

		if dFound == -1 {
			parts = append(parts, diffPart{op: diffDelete, lines: midA})
			parts = append(parts, diffPart{op: diffInsert, lines: midB})
		} else {
			x := n
			y := m
			var revParts []diffPart

			addRevLine := func(op diffOp, line string) {
				if len(revParts) > 0 && revParts[len(revParts)-1].op == op {
					revParts[len(revParts)-1].lines = append([]string{line}, revParts[len(revParts)-1].lines...)
				} else {
					revParts = append(revParts, diffPart{op: op, lines: []string{line}})
				}
			}

			for d := dFound; d > 0; d-- {
				vSnap := trace[d]
				k := x - y
				var prevK int
				if k == -d || (k != d && vSnap[k-1+offset] < vSnap[k+1+offset]) {
					prevK = k + 1
				} else {
					prevK = k - 1
				}
				prevX := vSnap[prevK+offset]
				prevY := prevX - prevK

				for x > prevX && y > prevY && midA[x-1] == midB[y-1] {
					addRevLine(diffEqual, midA[x-1])
					x--
					y--
				}
				if x == prevX {
					addRevLine(diffInsert, midB[y-1])
					y--
				} else {
					addRevLine(diffDelete, midA[x-1])
					x--
				}
			}
			for x > 0 && y > 0 && midA[x-1] == midB[y-1] {
				addRevLine(diffEqual, midA[x-1])
				x--
				y--
			}

			for i := len(revParts) - 1; i >= 0; i-- {
				parts = append(parts, revParts[i])
			}
		}
	}

	if suffixLen > 0 {
		parts = append(parts, diffPart{op: diffEqual, lines: a[len(a)-suffixLen:]})
	}

	var merged []diffPart
	for _, p := range parts {
		if len(p.lines) == 0 {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].op == p.op {
			merged[len(merged)-1].lines = append(merged[len(merged)-1].lines, p.lines...)
		} else {
			merged = append(merged, p)
		}
	}
	return merged
}

func generateDisplayDiff(oldContent, newContent string, contextLines int) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	parts := computeMyersDiff(oldLines, newLines)
	if len(parts) == 0 {
		return ""
	}

	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	width := len(strconv.Itoa(maxLineNum))
	if width < 4 {
		width = 4
	}

	var sb strings.Builder
	oldLineNum := 1
	newLineNum := 1
	lastWasChange := false

	for i := 0; i < len(parts); i++ {
		part := parts[i]

		if part.op == diffInsert || part.op == diffDelete {
			for _, line := range part.lines {
				if part.op == diffInsert {
					sb.WriteString(fmt.Sprintf("\x1b[32m%-*d + %s\x1b[0m\n", width, newLineNum, line))
					newLineNum++
				} else {
					sb.WriteString(fmt.Sprintf("\x1b[31m%-*d - %s\x1b[0m\n", width, oldLineNum, line))
					oldLineNum++
				}
			}
			lastWasChange = true
		} else {
			raw := part.lines
			nextPartIsChange := i < len(parts)-1 && (parts[i+1].op == diffInsert || parts[i+1].op == diffDelete)
			hasLeadingChange := lastWasChange
			hasTrailingChange := nextPartIsChange

			if hasLeadingChange && hasTrailingChange {
				if len(raw) <= contextLines*2 {
					for _, line := range raw {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}
				} else {
					leadingLines := raw[:contextLines]
					trailingLines := raw[len(raw)-contextLines:]
					skippedLines := len(raw) - len(leadingLines) - len(trailingLines)

					for _, line := range leadingLines {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}

					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines

					for _, line := range trailingLines {
						sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
						oldLineNum++
						newLineNum++
					}
				}
			} else if hasLeadingChange {
				shownLines := raw
				if len(shownLines) > contextLines {
					shownLines = raw[:contextLines]
				}
				skippedLines := len(raw) - len(shownLines)

				for _, line := range shownLines {
					sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
					oldLineNum++
					newLineNum++
				}

				if skippedLines > 0 {
					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines
				}
			} else if hasTrailingChange {
				skippedLines := len(raw) - contextLines
				if skippedLines < 0 {
					skippedLines = 0
				}
				if skippedLines > 0 {
					sb.WriteString(fmt.Sprintf("%-*s   ...\n", width, ""))
					oldLineNum += skippedLines
					newLineNum += skippedLines
				}

				for _, line := range raw[skippedLines:] {
					sb.WriteString(fmt.Sprintf("%-*d   %s\n", width, oldLineNum, line))
					oldLineNum++
					newLineNum++
				}
			} else {
				oldLineNum += len(raw)
				newLineNum += len(raw)
			}

			lastWasChange = false
		}
	}

	return sb.String()
}
