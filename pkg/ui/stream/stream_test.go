package stream_test

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/ui/stream"
	"loop/pkg/ui/style"
)

func TestJSONStreamParserBasic(t *testing.T) {
	theme := style.GetTheme("tokyonight")

	t.Run("bash command line streaming", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "bash"

		var buf bytes.Buffer
		p.Feed(`{"command": "echo hello"}`, &buf, theme)
		got := style.StripAnsi(buf.String())
		if !strings.Contains(got, "$ echo hello") {
			t.Errorf("expected bash command in output, got: %q", got)
		}
	})

	t.Run("suppresses content when streamWrites=false", func(t *testing.T) {
		p := stream.NewJSONStreamParser(false)
		p.ActiveToolName = "write"

		var buf bytes.Buffer
		p.Feed(`{"write_content": "secret text"}`, &buf, theme)
		got := buf.String()
		if strings.Contains(got, "secret text") {
			t.Errorf("expected content to be suppressed, got: %q", got)
		}
	})

	t.Run("streams content when streamWrites=true", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"

		var buf bytes.Buffer
		p.Feed(`{"write_content": "hello world"}`, &buf, theme)
		got := buf.String()
		if !strings.Contains(got, "hello world") {
			t.Errorf("expected content to be streamed, got: %q", got)
		}
	})
}

func TestLiveMarkdownRenderer(t *testing.T) {
	theme := style.GetTheme("tokyonight")
	var buf bytes.Buffer
	r := stream.NewLiveMarkdownRenderer(&buf, theme)

	r.Write("### Header Test\n")
	r.Write("This is **bold** and *italic* text.\n")
	r.Write("- Item 1\n")
	r.Write("1. Step 1\n")
	r.Flush()

	out := buf.String()
	if !strings.Contains(out, "Header Test") {
		t.Errorf("expected Header Test in output, got %q", out)
	}
	if !strings.Contains(out, "bold") {
		t.Errorf("expected bold in output, got %q", out)
	}
	if !strings.Contains(out, "Item 1") {
		t.Errorf("expected Item 1 in output, got %q", out)
	}
	if !strings.Contains(out, "Step 1") {
		t.Errorf("expected Step 1 in output, got %q", out)
	}
}

func TestLanguageDetection(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"package main\n\nfunc main() {}", "go"},
		{"def foo():\n    pass", "python"},
		{"fn main() {\n}", "rust"},
		{"SELECT * FROM users;", "sql"},
		{"#!/bin/bash\necho 1", "bash"},
	}

	for _, c := range cases {
		got := stream.DetectLangFromContent(c.input)
		if got != c.expected {
			t.Errorf("DetectLangFromContent(%q) = %q, want %q", c.input, got, c.expected)
		}
	}

	pathCases := []struct {
		path     string
		expected string
	}{
		{"main.go", "go"},
		{"app.py", "py"},
		{"Cargo.toml", "toml"},
		{"Makefile", "makefile"},
		{"Dockerfile", "dockerfile"},
	}

	for _, c := range pathCases {
		got := stream.DetectLangFromPath(c.path)
		if got != c.expected {
			t.Errorf("DetectLangFromPath(%q) = %q, want %q", c.path, got, c.expected)
		}
	}
}
