package file

import (
	"strings"
	"testing"
)

func TestValidateSyntax_Go(t *testing.T) {
	validGo := []byte(`package main

func main() {
	println("hello")
}
`)
	if warn := ValidateSyntax("main.go", validGo); warn != "" {
		t.Errorf("expected no warning for valid Go, got %q", warn)
	}

	invalidGo := []byte(`package main

func main() {
	println("unclosed string)
}
`)
	warn := ValidateSyntax("main.go", invalidGo)
	if warn == "" {
		t.Errorf("expected syntax warning for invalid Go, got empty")
	}
	if !strings.Contains(warn, "Go syntax error detected in main.go") {
		t.Errorf("expected warning mentioning main.go, got %q", warn)
	}
}

func TestValidateSyntax_JSON(t *testing.T) {
	validJSON := []byte(`{"key": "value", "count": 42}`)
	if warn := ValidateSyntax("config.json", validJSON); warn != "" {
		t.Errorf("expected no warning for valid JSON, got %q", warn)
	}

	invalidJSON := []byte(`{"key": "value", trailing_comma,}`)
	warn := ValidateSyntax("config.json", invalidJSON)
	if warn == "" {
		t.Errorf("expected syntax warning for invalid JSON, got empty")
	}
	if !strings.Contains(warn, "Invalid JSON syntax detected in config.json") {
		t.Errorf("expected warning mentioning config.json, got %q", warn)
	}
}

func TestValidateSyntax_Unhandled(t *testing.T) {
	text := []byte("plain text content")
	if warn := ValidateSyntax("notes.txt", text); warn != "" {
		t.Errorf("expected no warning for txt file, got %q", warn)
	}
}
