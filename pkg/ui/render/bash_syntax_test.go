package render

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv("NO_COLOR")
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Setenv("COLORTERM", "truecolor")
	os.Exit(m.Run())
}

func distinctColors(s string) int {
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

func TestFormatBashCommandLineToggle(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	cmd := "cd /workspace/skaters && python3 - <<'PY'\nimport ast\nprint('ok')\nPY"
	sym := RenderToolSymbol("bash", ToolStatusSuccess, theme)

	colored := FormatBashCommandLineStyled(sym, cmd, theme, true)
	plain := FormatBashCommandLineStyled(sym, cmd, theme, false)

	if style.StripAnsi(colored) != style.StripAnsi(plain) {
		t.Fatalf("stripped command text differs:\ncolored: %q\nplain:   %q", style.StripAnsi(colored), style.StripAnsi(plain))
	}
	if n := distinctColors(colored); n < 4 {
		t.Fatalf("expected rich syntax colors when enabled, got %d: %q", n, colored)
	}
	// Disabled should only use the symbol color + flat theme.Text color (at most 2).
	if n := distinctColors(plain); n > 2 {
		t.Fatalf("expected flat text color when disabled, got %d: %q", n, plain)
	}
}

func TestRenderToolOutputBashSyntaxToggleWithExitCodeFooter(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	args := `{"command":"curl -s https://api.example.com"}`
	output := "{\"status\":\"ok\",\"count\":3}\n\n[Command exit code: 0]"

	var coloredBuf, plainBuf bytes.Buffer
	RenderToolOutputWithOptions(&coloredBuf, output, false, false, theme, "bash", args, false, true)
	RenderToolOutputWithOptions(&plainBuf, output, false, false, theme, "bash", args, false, false)

	colored := coloredBuf.String()
	plain := plainBuf.String()

	if !strings.Contains(colored, "[Command exit code: 0]") || !strings.Contains(plain, "[Command exit code: 0]") {
		t.Fatalf("expected exit code footer preserved in both modes")
	}
	// Colored mode highlights JSON keys/values/numbers with multiple colors plus muted footer.
	if n := distinctColors(colored); n < 3 {
		t.Fatalf("expected syntax-colored JSON bash result, got %d colors: %q", n, colored)
	}
	// Disabled mode uses only theme.Text + theme.TextMuted (2 colors).
	if n := distinctColors(plain); n > 2 {
		t.Fatalf("expected plain bash result when disabled, got %d colors: %q", n, plain)
	}
}

func TestPrintSessionHistoryRespectsBashSyntaxColorConfig(t *testing.T) {
	theme := style.ResolveConfiguredTheme("studio", "auto")
	msgs := []db.Message{
		{Role: "user", Content: "check diff"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: db.ToolFunction{
					Name:      "bash",
					Arguments: `{"command":"git diff"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call-1",
			Name:       "bash",
			Content:    "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n\n[Command exit code: 0]",
		},
	}

	cfgOn := config.DefaultConfig()
	cfgOn.BashSyntaxColor = true
	var onBuf bytes.Buffer
	PrintSessionHistory(&onBuf, msgs, theme, cfgOn, false, false, HistoryRenderHooks{})

	cfgOff := config.DefaultConfig()
	cfgOff.BashSyntaxColor = false
	var offBuf bytes.Buffer
	PrintSessionHistory(&offBuf, msgs, theme, cfgOff, false, false, HistoryRenderHooks{})

	if distinctColors(onBuf.String()) <= distinctColors(offBuf.String()) {
		t.Fatalf("expected enabled bash_syntax_color to produce more syntax colors than disabled (%d vs %d)",
			distinctColors(onBuf.String()), distinctColors(offBuf.String()))
	}
}
