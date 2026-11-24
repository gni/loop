package syntax

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv("NO_COLOR")
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Setenv("COLORTERM", "truecolor")
	os.Exit(m.Run())
}

func distinctANSISequences(s string) int {
	seen := make(map[string]bool)
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			end := style.SkipAnsiEscape(s, i)
			seq := s[i:end]
			if seq != "\x1b[0m" {
				seen[seq] = true
			}
			i = end
			continue
		}
		i++
	}
	return len(seen)
}

func TestHighlightBashCommandLinesPythonHeredoc(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	lines := []string{
		"cd /workspace/skaters && python3 - <<'PY'",
		"import ast",
		"from pathlib import Path",
		"root=Path('backend/app/modules')",
		"for p in sorted(root.rglob('handler.py')):",
		" t=ast.parse(p.read_text())",
		" calls=[n for n in ast.walk(t) if isinstance(n,ast.Call) and isinstance(n.func,ast.Attribute) and n.func.attr in {'execute','scalar','scalars','add','delete','get','flush'} and isinstance(n.func.value,ast.Attribute) and n.func.value.attr=='session']",
		" if calls: print(p.parent.relative_to(root),len(calls))",
		"print('Endpoints:',len(list(root.rglob('endpoint.py'))),'Handlers:',len(list(root.rglob('handler.py'))))",
		"PY",
	}
	styled := HighlightBashCommandLines(lines, theme)
	if len(styled) != len(lines) {
		t.Fatalf("expected %d lines, got %d", len(lines), len(styled))
	}
	for i, want := range lines {
		if got := style.StripAnsi(styled[i]); got != want {
			t.Fatalf("line %d stripped text mismatch: got %q, want %q", i, got, want)
		}
	}
	// Shell header line should have multiple syntax colors (e.g., && and <<'PY').
	if n := distinctANSISequences(styled[0]); n < 2 {
		t.Fatalf("expected multi-color bash highlighting on header line, got %d colors: %q", n, styled[0])
	}
	// Python heredoc lines should have real Python token syntax colors, not flat string color.
	if n := distinctANSISequences(styled[3]); n < 3 {
		t.Fatalf("expected Python syntax highlighting on root=Path(...), got %d colors: %q", n, styled[3])
	}
	if n := distinctANSISequences(styled[6]); n < 3 {
		t.Fatalf("expected Python syntax highlighting on long comprehension line, got %d colors: %q", n, styled[6])
	}
	// Closing PY delimiter should be styled muted and exit heredoc mode cleanly.
	if !strings.Contains(styled[9], "\x1b[") {
		t.Fatalf("expected closing heredoc delimiter to be styled: %q", styled[9])
	}
}

func TestDetectHeredocStartVariantsAndFalsePositives(t *testing.T) {
	cases := []struct {
		line      string
		wantOK    bool
		wantDelim string
		wantStrip bool
		wantLang  string
	}{
		{"cd /app && python3 - <<'PY'", true, "PY", false, "python"},
		{"uv run python - <<-EOF", true, "EOF", true, "python"},
		{"node - <<'JS'", true, "JS", false, "javascript"},
		{"cat <<'EOF' > config.yaml", true, "EOF", false, "YAML"},
		{"cat > src/main.go <<'GO'", true, "GO", false, "Go"},
		{"sqlite3 app.db <<'SQL'", true, "SQL", false, "sql"},
		{"jq '.items' <<'EOF'", true, "EOF", false, "json"},
		{"bash <<'SH'", true, "SH", false, "bash"},
		{"echo hello <<< 'here string'", false, "", false, ""},
		{"echo $(( 1 << 2 ))", false, "", false, ""},
		{"echo \"<<'PY'\"", false, "", false, ""},
	}
	for _, tc := range cases {
		delim, strip, lang, ok := detectHeredocStart(tc.line)
		if ok != tc.wantOK {
			t.Errorf("%q: ok=%v, want %v (delim=%q lang=%q)", tc.line, ok, tc.wantOK, delim, lang)
			continue
		}
		if !tc.wantOK {
			continue
		}
		if delim != tc.wantDelim || strip != tc.wantStrip || !strings.EqualFold(lang, tc.wantLang) {
			t.Errorf("%q: got delim=%q strip=%v lang=%q; want delim=%q strip=%v lang=%q",
				tc.line, delim, strip, lang, tc.wantDelim, tc.wantStrip, tc.wantLang)
		}
	}
}

func TestShellOutputWriterHighlightsDiffGrepAndStreamedJSON(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")

	t.Run("git diff", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewShellOutputWriter(&buf, "git diff", theme)
		input := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n"
		for _, line := range strings.SplitAfter(input, "\n") {
			if line != "" {
				_, _ = w.Write([]byte(line))
			}
		}
		got := buf.String()
		if style.StripAnsi(got) != input {
			t.Fatalf("strip mismatch: got %q want %q", style.StripAnsi(got), input)
		}
		if distinctANSISequences(got) < 2 {
			t.Fatalf("expected multi-color diff syntax output, got: %q", got)
		}
	})

	t.Run("streamed multiline json colors first line immediately", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewShellOutputWriter(&buf, "curl -s https://example.com", theme)
		chunks := []string{"{\n", "  \"name\": \"loop\",\n", "  \"ok\": true\n", "}\n"}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
		}
		got := buf.String()
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) < 3 {
			t.Fatalf("unexpected lines: %v", lines)
		}
		if !strings.Contains(lines[1], "\x1b[") {
			t.Fatalf("expected streamed JSON body line 2 to be colored before closing brace, got: %q", lines[1])
		}
		if style.StripAnsi(got) != strings.Join(chunks, "") {
			t.Fatalf("strip mismatch on streamed JSON")
		}
	})

	t.Run("bash grep highlights path and code separately", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewShellOutputWriter(&buf, "rg -n func pkg/", theme)
		input := "pkg/main.go:12:func main() {\n"
		_, _ = w.Write([]byte(input))
		got := buf.String()
		if style.StripAnsi(got) != input {
			t.Fatalf("strip mismatch: got %q want %q", style.StripAnsi(got), input)
		}
		if distinctANSISequences(got) < 3 {
			t.Fatalf("expected path, line meta, and Go keyword colors, got: %q", got)
		}
	})

	t.Run("plain log with brackets is not misdetected as JSON", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewShellOutputWriter(&buf, "go test ./...", theme)
		input := "[INFO] running tests\nok  \tloop/pkg/ui\t0.10s\n"
		_, _ = w.Write([]byte(input))
		if got := buf.String(); got != input {
			t.Fatalf("expected plain log untouched, got %q", got)
		}
	})
}

func TestSplitBashExitCodeFooterAndLanguageDetection(t *testing.T) {
	raw := "{\"ok\": true}\n\n[Command exit code: 0]"
	body, footer := SplitBashExitCodeFooter(raw)
	if body != "{\"ok\": true}" || footer != "[Command exit code: 0]" {
		t.Fatalf("unexpected split: body=%q footer=%q", body, footer)
	}
	if lang := ShellOutputLanguage("echo ok", raw); lang != "json" {
		t.Fatalf("expected json language with exit code footer, got %q", lang)
	}
	if lang := ShellOutputLanguage("git show HEAD:pkg/app.py", "def f(): pass\n\n[Command exit code: 0]"); !strings.EqualFold(lang, "Python") {
		t.Fatalf("expected Python for git show HEAD:pkg/app.py, got %q", lang)
	}
}
