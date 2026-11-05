package file

import (
	"strings"
	"testing"
)

func TestLocateEditMatch_WithLineNumbersInOldText(t *testing.T) {
	content := `package main

import "fmt"

func Hello() {
	fmt.Println("hello world")
	fmt.Println("welcome to loop")
}

func Goodbye() {
	fmt.Println("goodbye")
}
`

	// Suppose the model copied lines from grep or compiler error with line numbers
	oldWithLineNumbers := ` 5: func Hello() {
 6: 	fmt.Println("hello world")
 7: 	fmt.Println("welcome to loop")
 8: }`

	newText := `func Hello() {
	fmt.Println("hello loop agent")
}`

	m, err := locateEditMatch(content, oldWithLineNumbers, newText)
	if err != nil {
		t.Fatalf("expected locateEditMatch to succeed with line numbers, got err: %v", err)
	}
	if m == nil {
		t.Fatalf("expected locateEditMatch to return match, got nil")
	}

	updated := content[:m.startByte] + m.newText + content[m.endByte:]
	if !strings.Contains(updated, "hello loop agent") {
		t.Errorf("expected updated content to contain replacement, got:\n%s", updated)
	}
	if strings.Contains(updated, "welcome to loop") {
		t.Errorf("expected welcome to loop to be replaced, got:\n%s", updated)
	}
}

func TestDetectOmissionPlaceholders(t *testing.T) {
	testCases := []struct {
		input       string
		shouldMatch bool
	}{
		{"// ... existing code ...", true},
		{"/* ... existing code ... */", true},
		{"# ... existing code ...", true},
		{"<!-- ... existing code ... -->", true},
		{"// ... rest of implementation", true},
		{"// ... other methods", true},
		{"// ... remaining functions", true},
		{"// [rest of code unchanged]", true},
		{"// ...", true},
		{"/* ... */", true},
		{"# ...", true},
		{"// TODO: implement remaining methods", true},
		{"// Valid normal comment explaining logic", false},
		{"x := 10 + 20", false},
		{"func DoWork() error { return nil }", false},
	}

	for _, tc := range testCases {
		matches := DetectOmissionPlaceholders(tc.input)
		matched := len(matches) > 0
		if matched != tc.shouldMatch {
			t.Errorf("DetectOmissionPlaceholders(%q) = %v; want %v (matches: %v)", tc.input, matched, tc.shouldMatch, matches)
		}
	}
}
