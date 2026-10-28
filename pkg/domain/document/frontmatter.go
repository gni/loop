package document

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"strings"
)

func Frontmatter(content string) (map[string]string, string, error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return nil, content, nil
	}
	end := strings.Index(normalized[4:], "\n---")
	if end < 0 {
		return nil, content, fmt.Errorf("unterminated YAML frontmatter")
	}
	if end > 64<<10 {
		return nil, content, fmt.Errorf("YAML frontmatter exceeds 64 KiB")
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(normalized[4:4+end]), &values); err != nil {
		return nil, content, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	metadata := make(map[string]string, len(values))
	for key, value := range values {
		metadata[key] = fmt.Sprint(value)
	}
	return metadata, strings.TrimPrefix(normalized[4+end+4:], "\n"), nil
}
