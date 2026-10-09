package file

import (
	"encoding/json"
	"regexp"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

// ReplaceEdit models candidate search and replacement structures across various LLM formatting dialects.
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

// EffectiveOldText resolves the first non-empty old text representation.
func (e ReplaceEdit) EffectiveOldText() string {
	return domaintool.FirstNonEmpty(
		e.OldText, e.OldTextSnake, e.OldString,
		e.TargetContent, e.TargetContentSnake, e.TargetContentPascal,
		e.Search, e.Old, e.OldStr,
	)
}

// EffectiveNewText resolves the first non-empty new text representation.
func (e ReplaceEdit) EffectiveNewText() string {
	return domaintool.FirstNonEmpty(
		e.NewText, e.NewTextSnake, e.NewString,
		e.ReplacementContent, e.ReplacementContentSnake, e.ReplacementContentPascal,
		e.Replace, e.New, e.NewStr,
	)
}

var pathKeyRegex = regexp.MustCompile(`"(?i)(?:path|filePath|file_path|targetFile|target_file|TargetFile|fileName|file_name|filename|file|target|AbsolutePath|absolute_path|write_path|writePath)"\s*:\s*"([^"]+)"`)

// ExtractFilePath searches candidate argument fields, JSON trees, and fallback regexes for a file path.
func ExtractFilePath(rawJSON string, candidatePaths ...string) string {
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

func extractFilePath(rawJSON string, candidatePaths ...string) string {
	return ExtractFilePath(rawJSON, candidatePaths...)
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

func extractEditFromMap(m map[string]interface{}) (ReplaceEdit, bool) {
	oldT := findStringInMap(m, "oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr")
	newT := findStringInMap(m, "newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr")
	if oldT != "" {
		return ReplaceEdit{OldText: oldT, NewText: newT}, true
	}
	return ReplaceEdit{}, false
}

func extractEditsFromGenericMap(m map[string]interface{}) []ReplaceEdit {
	var results []ReplaceEdit

	listKeys := []string{"updates", "edits", "replacements"}
	for _, lk := range listKeys {
		if val, ok := m[lk]; ok {
			if slice, ok := val.([]interface{}); ok {
				for _, item := range slice {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if edit, ok := extractEditFromMap(itemMap); ok {
							results = append(results, edit)
						}
					}
				}
			} else if itemMap, ok := val.(map[string]interface{}); ok {
				if edit, ok := extractEditFromMap(itemMap); ok {
					results = append(results, edit)
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

	if edit, ok := extractEditFromMap(m); ok {
		results = append(results, edit)
	}

	return results
}

// ExtractEdits extracts old/new text edit pairs from various argument variations.
func ExtractEdits(rawJSON string, candidateEdits ...[]ReplaceEdit) []ReplaceEdit {
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
				if edit, ok := extractEditFromMap(itemMap); ok {
					merged = append(merged, edit)
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

func extractEdits(rawJSON string, candidateEdits ...[]ReplaceEdit) []ReplaceEdit {
	return ExtractEdits(rawJSON, candidateEdits...)
}

// ExtractFileContent extracts complete file content from argument payloads.
func ExtractFileContent(rawJSON string, candidateContents ...string) (string, bool) {
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

func extractFileContent(rawJSON string, candidateContents ...string) (string, bool) {
	return ExtractFileContent(rawJSON, candidateContents...)
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
