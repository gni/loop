package stream

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

func countColors(s string) int {
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

func TestStreamRendererLiveBashHeredocSyntaxAndToggle(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	command := "cd /workspace/skaters && python3 - <<'PY'\nimport ast\nfrom pathlib import Path\ncalls=[n for n in ast.walk(t) if isinstance(n,ast.Call) and isinstance(n.func,ast.Attribute) and n.func.attr in {'execute','scalar','scalars','add','delete','get','flush'}]\nPY"

	run := func(enabled bool) string {
		var buf bytes.Buffer
		sr := NewStreamRenderer(&buf, theme, false, true, StreamOptions{
			Markdown:        true,
			BashSyntaxColor: enabled,
		})
		// Stream in small uneven chunks to verify token/line boundaries.
		for i := 0; i < len(command); i += 17 {
			end := min(i+17, len(command))
			sr.ShowLiveToolContent(0, "bash", "", command[i:end])
		}
		sr.EndLiveToolContent()
		return buf.String()
	}

	colored := run(true)
	plain := run(false)

	if style.StripAnsi(colored) != style.StripAnsi(plain) {
		t.Fatalf("stripped live bash output mismatch:\ncolored: %q\nplain:   %q", style.StripAnsi(colored), style.StripAnsi(plain))
	}
	if !strings.Contains(style.StripAnsi(colored), "  import ast\n") {
		t.Fatalf("expected continuation line indent in live bash output, got: %q", style.StripAnsi(colored))
	}
	if n := countColors(colored); n < 4 {
		t.Fatalf("expected live bash heredoc syntax colors when enabled, got %d: %q", n, colored)
	}
	if n := countColors(plain); n > 2 {
		t.Fatalf("expected flat colors when bash_syntax_color is disabled, got %d: %q", n, plain)
	}
}

func TestStreamRendererNewShellOutputWriterRespectsToggle(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	args := `{"command":"cat main.go"}`
	sample := "package main\nfunc main() {}\n"

	var onBuf bytes.Buffer
	srOn := NewStreamRenderer(&onBuf, theme, false, true, StreamOptions{BashSyntaxColor: true})
	var outOn bytes.Buffer
	_, _ = srOn.NewShellOutputWriter(&outOn, args).Write([]byte(sample))
	if !strings.Contains(outOn.String(), "\x1b[") {
		t.Fatalf("expected syntax-colored output when BashSyntaxColor=true, got %q", outOn.String())
	}

	var offBuf bytes.Buffer
	srOff := NewStreamRenderer(&offBuf, theme, false, true, StreamOptions{BashSyntaxColor: false})
	var outOff bytes.Buffer
	_, _ = srOff.NewShellOutputWriter(&outOff, args).Write([]byte(sample))
	if outOff.String() != sample {
		t.Fatalf("expected raw passthrough when BashSyntaxColor=false, got %q", outOff.String())
	}
}
