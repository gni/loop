package tool

import (
	"strings"
	"testing"
)

func TestRepairJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty input",
			input:    "",
			expected: "{}",
		},
		{
			name:     "markdown fence wrapper",
			input:    "```json\n{\"command\": \"ls\"}\n```",
			expected: "{\"command\": \"ls\"}",
		},
		{
			name:     "unescaped newline in string",
			input:    "{\"text\": \"hello\nworld\"}",
			expected: "{\"text\": \"hello\\nworld\"}",
		},
		{
			name:     "unclosed brace",
			input:    "{\"command\": \"echo test\"",
			expected: "{\"command\": \"echo test\"}",
		},
		{
			name:     "unclosed bracket",
			input:    "{\"items\": [1, 2, 3",
			expected: "{\"items\": [1, 2, 3]}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RepairJSON(tc.input)
			if !strings.Contains(got, tc.expected) && got != tc.expected {
				t.Fatalf("RepairJSON(%q) = %q; expected %q", tc.input, got, tc.expected)
			}
		})
	}
}
