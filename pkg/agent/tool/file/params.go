package file

import (
	"encoding/json"
	"regexp"
	"strings"

	domaintool "loop/pkg/domain/tool"
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
	Instruction              string `json:"instruction,omitempty"`
	Instructions             string `json:"instructions,omitempty"`
	Description              string `json:"description,omitempty"`
	Path                     string `json:"path"`
	File                     string `json:"file"`
	FilePath                 string `json:"file_path"`
	TargetFile               string `json:"target_file"`
}

func (e ReplaceEdit) EffectiveOldText() string {
	return domaintool.FirstNonEmpty(
		e.OldText, e.OldTextSnake, e.OldString,
		e.TargetContent, e.TargetContentSnake, e.TargetContentPascal,
		e.Search, e.Old, e.OldStr,
	)
}

func (e ReplaceEdit) EffectiveNewText() string {
	return domaintool.FirstNonEmpty(
		e.NewText, e.NewTextSnake, e.NewString,
		e.ReplacementContent, e.ReplacementContentSnake, e.ReplacementContentPascal,
		e.Replace, e.New, e.NewStr,
	)
}

var (
	pathKeyRegex = regexp.MustCompile(`"(?i)(?:path|filePath|file_path|targetFile|target_file|TargetFile|fileName|file_name|filename|file|target|AbsolutePath|absolute_path|write_path|writePath)"\s*:\s*"([^"]+)"`)
	pathMapKeys  = []string{
		"path", "Path", "file_path", "filePath", "FilePath",
		"target_file", "targetFile", "TargetFile",
		"filename", "fileName", "FileName", "file_name",
		"file", "File", "target", "Target",
		"AbsolutePath", "absolute_path", "absolutepath",
		"write_path", "writePath", "WritePath",
		"dest", "destination", "dest_file", "location", "uri", "URI",
	}
	wrapperKeys  = []string{"input", "parameters", "arguments", "edit", "write", "read", "params", "tool_input"}
	editListKeys = []string{"updates", "edits", "replacements"}
	oldTextKeys  = []string{"oldText", "old_text", "oldTextSnake", "old_string", "oldString", "targetContent", "target_content", "TargetContent", "search", "old", "old_str", "oldStr"}
	newTextKeys  = []string{"newText", "new_text", "newTextSnake", "new_string", "newString", "replacementContent", "replacement_content", "ReplacementContent", "replace", "new", "new_str", "newStr"}
	contentKeys  = []string{"content", "Content", "code_content", "codeContent", "CodeContent", "write_content", "writeContent", "file_content", "fileContent", "text", "Text", "body", "Body", "data", "Data", "contents", "Contents"}
)

func parseGenericJSON(trimmed string) (map[string]interface{}, []interface{}) {
	if strings.HasPrefix(trimmed, "[") {
		var slice []interface{}
		if err := json.Unmarshal([]byte(trimmed), &slice); err == nil && len(slice) > 0 {
			return nil, slice
		}
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil && m != nil {
			return m, nil
		}
	}
	return nil, nil
}

func ExtractWrappedFloats(rawJSON string, keys ...string) map[string]float64 {
	generic, _ := parseGenericJSON(strings.TrimSpace(rawJSON))
	if generic == nil {
		return nil
	}
	out := make(map[string]float64, len(keys))
	for _, w := range wrapperKeys {
		sub, ok := generic[w].(map[string]interface{})
		if !ok {
			continue
		}
		for _, k := range keys {
			if _, exists := out[k]; !exists {
				if val, ok := sub[k].(float64); ok && val != 0 {
					out[k] = val
				}
			}
		}
	}
	return out
}

func ExtractFilePath(rawJSON string, candidatePaths ...string) string {
	for _, p := range candidatePaths {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			return trimmed
		}
	}

	trimmedJSON := strings.TrimSpace(rawJSON)
	if trimmedJSON == "" {
		return ""
	}

	genericMap, genericSlice := parseGenericJSON(trimmedJSON)
	if len(genericSlice) > 0 {
		if firstItem, ok := genericSlice[0].(map[string]interface{}); ok {
			if p := findPathInGenericMap(firstItem); p != "" {
				return p
			}
		}
	}
	if genericMap != nil {
		if p := findPathInGenericMap(genericMap); p != "" {
			return p
		}
	}

	if matches := pathKeyRegex.FindStringSubmatch(trimmedJSON); len(matches) > 1 {
		if res := strings.TrimSpace(matches[1]); res != "" {
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
	if s := findTrimmedStringInMap(m, pathMapKeys...); s != "" {
		return s
	}
	for _, w := range wrapperKeys {
		if subMap, ok := m[w].(map[string]interface{}); ok {
			if p := findPathInGenericMap(subMap); p != "" {
				return p
			}
		}
	}
	for _, lk := range editListKeys {
		switch val := m[lk].(type) {
		case []interface{}:
			if len(val) > 0 {
				if firstItem, ok := val[0].(map[string]interface{}); ok {
					if p := findPathInGenericMap(firstItem); p != "" {
						return p
					}
				}
			}
		case map[string]interface{}:
			if p := findPathInGenericMap(val); p != "" {
				return p
			}
		}
	}
	return ""
}

func extractEditFromMap(m map[string]interface{}) (ReplaceEdit, bool) {
	oldT := findStringInMap(m, oldTextKeys...)
	newT := findStringInMap(m, newTextKeys...)
	if oldT != "" {
		return ReplaceEdit{OldText: oldT, NewText: newT}, true
	}
	return ReplaceEdit{}, false
}

func extractEditsFromGenericMap(m map[string]interface{}) []ReplaceEdit {
	var results []ReplaceEdit
	for _, lk := range editListKeys {
		switch val := m[lk].(type) {
		case []interface{}:
			for _, item := range val {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if edit, ok := extractEditFromMap(itemMap); ok {
						results = append(results, edit)
					}
				}
			}
		case map[string]interface{}:
			if edit, ok := extractEditFromMap(val); ok {
				results = append(results, edit)
			}
		}
	}
	if len(results) > 0 {
		return results
	}

	for _, w := range wrapperKeys {
		if subMap, ok := m[w].(map[string]interface{}); ok {
			if subEdits := extractEditsFromGenericMap(subMap); len(subEdits) > 0 {
				return subEdits
			}
		}
	}

	if edit, ok := extractEditFromMap(m); ok {
		return []ReplaceEdit{edit}
	}
	return nil
}

func ExtractEdits(rawJSON string, candidateEdits ...[]ReplaceEdit) []ReplaceEdit {
	var merged []ReplaceEdit
	for _, group := range candidateEdits {
		for _, e := range group {
			if old := e.EffectiveOldText(); old != "" {
				merged = append(merged, ReplaceEdit{OldText: old, NewText: e.EffectiveNewText()})
			}
		}
	}
	if len(merged) > 0 {
		return merged
	}

	genericMap, genericSlice := parseGenericJSON(strings.TrimSpace(rawJSON))
	if len(genericSlice) > 0 {
		for _, item := range genericSlice {
			if itemMap, ok := item.(map[string]interface{}); ok {
				merged = append(merged, extractEditsFromGenericMap(itemMap)...)
			}
		}
		return merged
	}
	if genericMap != nil {
		return extractEditsFromGenericMap(genericMap)
	}
	return nil
}

func ExtractFileContent(rawJSON string, candidateContents ...string) (string, bool) {
	for _, c := range candidateContents {
		if c != "" {
			return c, true
		}
	}
	if genericMap, _ := parseGenericJSON(strings.TrimSpace(rawJSON)); genericMap != nil {
		return findContentInGenericMap(genericMap)
	}
	return "", false
}

func findContentInGenericMap(m map[string]interface{}) (string, bool) {
	for _, k := range contentKeys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	for _, w := range wrapperKeys {
		if subMap, ok := m[w].(map[string]interface{}); ok {
			if c, ok := findContentInGenericMap(subMap); ok {
				return c, true
			}
		}
	}
	return "", false
}

func findStringInMap(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func findTrimmedStringInMap(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}