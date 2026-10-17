package file

import (
	"encoding/json"
	"strings"
)

// ExtractStreamingFilePath accepts a path only after its JSON string is complete.
// The regular file-path extractor repairs incomplete JSON, which is unsafe here.
func ExtractStreamingFilePath(arguments string) string {
	for i := 0; i < len(arguments); {
		if arguments[i] != '"' {
			i++
			continue
		}

		key, next, complete := readStreamingJSONString(arguments, i)
		if !complete {
			return ""
		}
		i = skipStreamingSpace(arguments, next)
		if i >= len(arguments) || arguments[i] != ':' {
			continue
		}

		isPath := false
		for _, knownKey := range pathMapKeys {
			if strings.EqualFold(key, knownKey) {
				isPath = true
				break
			}
		}
		if !isPath {
			continue
		}

		i = skipStreamingSpace(arguments, i+1)
		if i >= len(arguments) || arguments[i] != '"' {
			return ""
		}
		value, next, complete := readStreamingJSONString(arguments, i)
		if !complete {
			return ""
		}
		if path := strings.TrimSpace(value); path != "" {
			return path
		}
		i = next
	}
	return ""
}

func skipStreamingSpace(value string, index int) int {
	for index < len(value) {
		switch value[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func readStreamingJSONString(value string, start int) (string, int, bool) {
	if start >= len(value) || value[start] != '"' {
		return "", start, false
	}
	escaped := false
	for end := start + 1; end < len(value); end++ {
		if escaped {
			escaped = false
			continue
		}
		if value[end] == '\\' {
			escaped = true
			continue
		}
		if value[end] != '"' {
			continue
		}
		var decoded string
		if err := json.Unmarshal([]byte(value[start:end+1]), &decoded); err != nil {
			return "", start, false
		}
		return decoded, end + 1, true
	}
	return "", start, false
}
