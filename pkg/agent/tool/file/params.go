package file

import (
	"encoding/json"
	"regexp"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type ReplaceEdit struct {
	OldText                  string `json:"oldText,omitempty"`
	NewText                  string `json:"newText"`
	OldTextSnake             string `json:"old_text,omitempty"`
	NewTextSnake             string `json:"new_text,omitempty"`
	OldString                string `json:"old_string,omitempty"`
	NewString                string `json:"new_string,omitempty"`
	TargetContent            string `json:"targetContent,omitempty"`
	TargetContentSnake       string `json:"target_content,omitempty"`
	TargetContentPascal      string `json:"TargetContent,omitempty"`
	ReplacementContent       string `json:"replacementContent,omitempty"`
	ReplacementContentSnake  string `json:"replacement_content,omitempty"`
	ReplacementContentPascal string `json:"ReplacementContent,omitempty"`
	Search                   string `json:"search,omitempty"`
	Replace                  string `json:"replace,omitempty"`
	Old                      string `json:"old,omitempty"`
	New                      string `json:"new,omitempty"`
	OldStr                   string `json:"old_str,omitempty"`
	NewStr                   string `json:"new_str,omitempty"`
	Instruction              string `json:"instruction,omitempty"`
	Instructions             string `json:"instructions,omitempty"`
	Description              string `json:"description,omitempty"`
	Path                     string `json:"path,omitempty"`
	File                     string `json:"file,omitempty"`
	FilePath                 string `json:"file_path,omitempty"`
	TargetFile               string `json:"target_file,omitempty"`
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
		if repaired := domaintool.RepairJSON(trimmed); repaired != trimmed {
			if err := json.Unmarshal([]byte(repaired), &slice); err == nil && len(slice) > 0 {
				return nil, slice
			}
		}
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &m); err == nil && m != nil {
			return m, nil
		}
		if repaired := domaintool.RepairJSON(trimmed); repaired != trimmed {
			if err := json.Unmarshal([]byte(repaired), &m); err == nil && m != nil {
				return m, nil
			}
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

// IsFilePathKey recognizes the same filename aliases used by file execution.
func IsFilePathKey(key string) bool {
	for _, candidate := range pathMapKeys {
		if strings.EqualFold(key, candidate) {
			return true
		}
	}
	return false
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

// ExtractEdits retains the legacy helper while using strict request parsing.
func ExtractEdits(rawJSON string, candidateEdits ...[]ReplaceEdit) []ReplaceEdit {
	if strings.TrimSpace(rawJSON) != "" {
		_, edits, err := ParseEditArguments(rawJSON)
		if err != nil {
			return nil
		}
		return edits
	}
	var edits []ReplaceEdit
	for _, group := range candidateEdits {
		edits = append(edits, group...)
	}
	return edits
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
