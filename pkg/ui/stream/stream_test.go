package stream_test

import (
	"bytes"
	"io"
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

func TestJSONStreamParserContentBeforePathStreaming(t *testing.T) {
	theme := style.GetTheme("tokyonight")

	t.Run("buffers content lines when path has not arrived and flushes title first when path arrives", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"

		var buf bytes.Buffer
		// First chunk: content only, path has not arrived
		p.Feed(`{"content": "package main\nfunc main() {\n`, &buf, theme)

		mid := style.StripAnsi(buf.String())
		// Bare "write" must NEVER be output before path is known
		if strings.Contains(mid, "write") {
			t.Fatalf("expected no premature bare title before path arrives, got: %q", mid)
		}
		if buf.Len() > 0 {
			t.Fatalf("expected content to be buffered until path is known, got: %q", mid)
		}

		// Second chunk: remaining content and path arrives
		p.Feed(`}\n", "path": "cmd/main.go"}`, &buf, theme)
		final := style.StripAnsi(buf.String())
		if !strings.Contains(final, "write cmd/main.go") {
			t.Fatalf("expected title with path, got: %q", final)
		}
		if !strings.Contains(final, "package main") || !strings.Contains(final, "func main()") {
			t.Fatalf("expected buffered content to be flushed, got: %q", final)
		}
		headerIdx := strings.Index(final, "write cmd/main.go")
		codeIdx := strings.Index(final, "package main")
		if headerIdx > codeIdx {
			t.Fatalf("expected header before code, got header at %d, code at %d", headerIdx, codeIdx)
		}
	})

	t.Run("never outputs bare write title even if writer does not support in-place replacement", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"

		// Use a custom writer that does not support in-place buffer modification or scroll replacement
		w := &struct{ io.Writer }{Writer: &bytes.Buffer{}}

		p.Feed(`{"content": "line1\nline2\n`, w, theme)
		before := style.StripAnsi(w.Writer.(*bytes.Buffer).String())
		if strings.Contains(before, "write") || len(before) > 0 {
			t.Fatalf("expected no output before path is known, got: %q", before)
		}

		// Now path arrives
		p.Feed(`", "path": "app.vue"}`, w, theme)
		after := style.StripAnsi(w.Writer.(*bytes.Buffer).String())
		if !strings.Contains(after, "write app.vue") {
			t.Fatalf("expected 'write app.vue' in output, got: %q", after)
		}
		if !strings.Contains(after, "line1") || !strings.Contains(after, "line2") {
			t.Fatalf("expected content flushed, got: %q", after)
		}
		countWrite := strings.Count(after, "write")
		if countWrite != 1 {
			t.Fatalf("expected exactly 1 title with filename, got %d in: %q", countWrite, after)
		}
	})

	t.Run("streams immediately when path arrives first in JSON", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"

		var buf bytes.Buffer
		p.Feed(`{"path": "cmd/main.go", "content": "package main\n`, &buf, theme)
		mid := style.StripAnsi(buf.String())
		if !strings.Contains(mid, "write cmd/main.go") {
			t.Fatalf("expected title with path on chunk 1, got: %q", mid)
		}
		if !strings.Contains(mid, "package main") {
			t.Fatalf("expected content line on chunk 1, got: %q", mid)
		}
		headerIdx := strings.Index(mid, "write cmd/main.go")
		codeIdx := strings.Index(mid, "package main")
		if headerIdx > codeIdx {
			t.Fatalf("expected header before code, got header at %d, code at %d", headerIdx, codeIdx)
		}
	})

	t.Run("streams raw content directly when tool target is set", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"
		p.Path = "frontend/src/App.vue"
		p.IsRawStream = true

		var buf bytes.Buffer
		p.FeedRaw("<script>\nimport { api } from './api.js';\n", &buf, theme)
		p.FeedRaw("export default {\n  name: 'App'\n};\n</script>\n", &buf, theme)

		out := style.StripAnsi(buf.String())
		if !strings.Contains(out, "write frontend/src/App.vue") {
			t.Fatalf("expected title with path, got: %q", out)
		}
		if !strings.Contains(out, "import { api }") || !strings.Contains(out, "name: 'App'") {
			t.Fatalf("expected raw code lines streamed, got: %q", out)
		}
		headerIdx := strings.Index(out, "write frontend/src/App.vue")
		codeIdx := strings.Index(out, "import { api }")
		if headerIdx > codeIdx {
			t.Fatalf("expected header before code, got header at %d, code at %d", headerIdx, codeIdx)
		}
	})

	t.Run("UTF-8 accents and unicode escapes in content", func(t *testing.T) {
		p := stream.NewJSONStreamParser(true)
		p.ActiveToolName = "write"
		p.TitlePrinted = true
		var buf bytes.Buffer

		input := `{"content":"café crème à la fête \u00e9\n"}`
		p.Feed(input, &buf, theme)

		out := style.StripAnsi(buf.String())
		if !strings.Contains(out, "café crème à la fête é") {
			t.Fatalf("expected preserved UTF-8 accents and unescaped \\u00e9, got: %q", out)
		}
	})
}
