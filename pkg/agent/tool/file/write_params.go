package file

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseWriteArguments requires one unambiguous path and an explicit string body.
// An empty content string is valid; missing or mistyped content is not.
func ParseWriteArguments(arguments string) (string, string, error) {
	if len(arguments) > 8<<20 {
		return "", "", fmt.Errorf("write arguments exceed 8 MiB")
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return "", "", fmt.Errorf("invalid write arguments: %w", err)
	}
	var path, content string
	hasContent := false
	for depth := 0; ; depth++ {
		if depth > 16 {
			return "", "", fmt.Errorf("write arguments are nested too deeply")
		}
		m, ok := value.(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("write arguments must be an object")
		}
		nextPath, hasPath, err := argumentString(m, pathMapKeys)
		if err != nil {
			return "", "", err
		}
		if hasPath {
			nextPath = strings.TrimSpace(nextPath)
			if nextPath == "" {
				return "", "", fmt.Errorf("path must not be empty")
			}
			if path != "" && path != nextPath {
				return "", "", fmt.Errorf("conflicting write paths")
			}
			path = nextPath
		}
		nextContent, found, err := argumentString(m, contentKeys)
		if err != nil {
			return "", "", err
		}
		if found {
			if hasContent && content != nextContent {
				return "", "", fmt.Errorf("conflicting write content")
			}
			content, hasContent = nextContent, true
		}
		var nested any
		nestedFound := false
		for _, key := range wrapperKeys {
			if v, exists := m[key]; exists {
				if nestedFound {
					return "", "", fmt.Errorf("use a single write wrapper")
				}
				nested, nestedFound = v, true
			}
		}
		if !nestedFound {
			break
		}
		value = nested
	}
	if path == "" {
		return "", "", fmt.Errorf("missing required argument: path")
	}
	if !hasContent {
		return "", "", fmt.Errorf("missing required argument: content (use an empty string to clear a file)")
	}
	return path, content, nil
}
