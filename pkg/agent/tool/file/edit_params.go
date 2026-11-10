package file

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxEdits = 256

// ParseEditArguments validates the whole request before any replacement is used.
// An explicitly empty newText deletes text; a missing replacement is an error.
func ParseEditArguments(arguments string) (string, []ReplaceEdit, error) {
	if len(arguments) > 8<<20 {
		return "", nil, fmt.Errorf("edit arguments exceed 8 MiB")
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return "", nil, fmt.Errorf("invalid edit arguments: %w", err)
	}
	path, edits, err := parseEditValue(value, "", 0)
	if err != nil {
		return "", nil, err
	}
	if path == "" {
		return "", nil, fmt.Errorf("missing required argument: path")
	}
	if len(edits) == 0 {
		return "", nil, fmt.Errorf("no edits specified to apply")
	}
	return path, edits, nil
}

func editString(m map[string]any, keys []string) (string, bool, error) {
	var result string
	found := false
	for _, key := range keys {
		v, exists := m[key]
		if !exists {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return "", false, fmt.Errorf("%s must be a string", key)
		}
		if found && s != result {
			return "", false, fmt.Errorf("conflicting aliases for %s", keys[0])
		}
		result, found = s, true
	}
	return result, found, nil
}

func parseEditValue(value any, inheritedPath string, depth int) (string, []ReplaceEdit, error) {
	if depth > 16 {
		return "", nil, fmt.Errorf("edit arguments are nested too deeply")
	}
	if entries, ok := value.([]any); ok {
		if len(entries) == 0 || len(entries) > maxEdits {
			return "", nil, fmt.Errorf("provide between 1 and %d edits", maxEdits)
		}
		path := inheritedPath
		var edits []ReplaceEdit
		for i, entry := range entries {
			p, group, err := parseEditValue(entry, inheritedPath, depth+1)
			if err != nil {
				return "", nil, fmt.Errorf("edit[%d]: %w", i, err)
			}
			if path == "" {
				path = p
			}
			edits = append(edits, group...)
			if len(edits) > maxEdits {
				return "", nil, fmt.Errorf("provide at most %d edits", maxEdits)
			}
		}
		return path, edits, nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("each edit must be an object")
	}
	path, hasPath, err := editString(m, pathMapKeys)
	if err != nil {
		return "", nil, err
	}
	if hasPath {
		path = strings.TrimSpace(path)
		if path == "" {
			return "", nil, fmt.Errorf("path must not be empty")
		}
	} else {
		path = inheritedPath
	}
	var nested any
	nestedFound := false
	for _, key := range append(append([]string(nil), editListKeys...), wrapperKeys...) {
		if v, exists := m[key]; exists {
			if nestedFound {
				return "", nil, fmt.Errorf("use a single edit list or wrapper")
			}
			nested, nestedFound = v, true
		}
	}
	old, hasOld, err := editString(m, oldTextKeys)
	if err != nil {
		return "", nil, err
	}
	newText, hasNew, err := editString(m, newTextKeys)
	if err != nil {
		return "", nil, err
	}
	if nestedFound {
		if hasOld || hasNew {
			return "", nil, fmt.Errorf("use either an edit list or a single replacement")
		}
		nestedPath, edits, err := parseEditValue(nested, path, depth+1)
		if path != "" {
			nestedPath = path
		}
		return nestedPath, edits, err
	}
	if !hasOld || old == "" {
		return "", nil, fmt.Errorf("oldText must be a nonempty string")
	}
	if !hasNew {
		return "", nil, fmt.Errorf("missing required argument: newText (use an empty string to delete text)")
	}
	return path, []ReplaceEdit{{OldText: old, NewText: newText, Path: path}}, nil
}
