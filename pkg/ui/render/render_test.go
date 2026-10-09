package render

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestRenderBanner(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{
		Endpoint: "http://localhost:8080",
		Model:    "test-model",
	}
	a := &agent.Agent{Config: cfg}
	theme := style.GetTheme("catppuccin")

	PrintBanner(&buf, a, theme)
	out := buf.String()
	if !strings.Contains(out, "loop v1.0.0") {
		t.Fatalf("expected banner to contain version, got: %s", out)
	}
	if !strings.Contains(out, "test-model") {
		t.Fatalf("expected banner to contain model, got: %s", out)
	}
}

func TestRenderHelp(t *testing.T) {
	var buf bytes.Buffer
	theme := style.GetTheme("catppuccin")

	RenderHelp(&buf, theme)
	out := buf.String()
	if !strings.Contains(out, "/config") || !strings.Contains(out, "/help") {
		t.Fatalf("expected help to list slash commands, got: %s", out)
	}
}

func TestRenderConfig(t *testing.T) {
	var buf bytes.Buffer
	cfg := &config.Config{
		Endpoint:           "http://localhost:8080",
		Model:              "test-model",
		ContextWindowLimit: 64000,
	}
	theme := style.GetTheme("catppuccin")

	RenderConfig(&buf, cfg, theme)
	out := buf.String()
	if !strings.Contains(out, "loop runtime settings") || !strings.Contains(out, "64000") {
		t.Fatalf("expected config table with 64000 tokens, got: %s", out)
	}
}

func TestToolGlyphsAndTargetExtraction(t *testing.T) {
	if glyph := GetToolGlyph("read"); glyph != "◈" {
		t.Fatalf("expected ◈ for read, got %s", glyph)
	}
	if glyph := GetToolGlyph("bash"); glyph != "$" {
		t.Fatalf("expected $ for bash, got %s", glyph)
	}

	target := ExtractToolTarget("read", `{"path": "pkg/main.go"}`)
	if target != "pkg/main.go" {
		t.Fatalf("expected target pkg/main.go, got: %s", target)
	}

	cmdTarget := ExtractToolTarget("bash", `{"command": "ls -la"}`)
	if cmdTarget != "ls -la" {
		t.Fatalf("expected command ls -la, got: %s", cmdTarget)
	}
}

func TestCalculatePromptLayout(t *testing.T) {
	layout := CalculatePromptLayout("> ", "hello world", 5, 80)
	if layout.TotalRows != 1 {
		t.Fatalf("expected TotalRows=1, got %d", layout.TotalRows)
	}
	if layout.CursorRow != 0 {
		t.Fatalf("expected CursorRow=0, got %d", layout.CursorRow)
	}
}

func TestPrintSessionHistory(t *testing.T) {
	var buf bytes.Buffer
	theme := style.GetTheme("catppuccin")
	cfg := &config.Config{
		ShowThinking: true,
	}
	messages := []db.Message{
		{Role: "user", Content: "Hello world"},
		{Role: "assistant", Content: "Hi there!"},
	}

	hooks := HistoryRenderHooks{
		RenderMarkdown: func(w io.Writer, content string, th style.UITheme) {
			_, _ = w.Write([]byte(content))
		},
	}

	PrintSessionHistory(&buf, messages, theme, cfg, false, false, hooks)
	out := buf.String()
	if !strings.Contains(out, "Hello world") || !strings.Contains(out, "Hi there!") {
		t.Fatalf("expected session history to contain messages, got: %s", out)
	}
}

func TestSanitizeTerminalTextAndRenderError(t *testing.T) {
	raw := "line1\r\n\x1b[2J\x1b[Hline2\tline3"
	sanitized := SanitizeTerminalText(raw)
	if strings.Contains(sanitized, "\r") {
		t.Fatalf("sanitized text contains carriage return: %q", sanitized)
	}
	if strings.Contains(sanitized, "\x1b") {
		t.Fatalf("sanitized text contains ANSI sequence: %q", sanitized)
	}
	if !strings.Contains(sanitized, "    line3") {
		t.Fatalf("sanitized text did not expand tab: %q", sanitized)
	}

	var buf bytes.Buffer
	theme := style.GetTheme("catppuccin")
	RenderGenerationError(&buf, "something failed", theme)
	out := buf.String()
	if !strings.Contains(out, "error during generation:") || !strings.Contains(out, "something failed") {
		t.Fatalf("unexpected error rendering: %q", out)
	}
}

func TestFormatStatusBar(t *testing.T) {
	theme := style.GetTheme("catppuccin")
	state := StatusBarState{
		Model:            "gpt-4o",
		PromptTokens:     1500,
		CompletionTokens: 250,
		ContextLimit:     128000,
		ShowTokens:       true,
		IsGenerating:     true,
		LastTps:          42.5,
		HasLastTps:       true,
		StartTime:        time.Now(),
	}

	left := FormatStatusBarLeft(state, theme, 100)
	cleanLeft := style.StripAnsi(left)
	if !strings.Contains(cleanLeft, "1.5k in") || !strings.Contains(cleanLeft, "250 out") {
		t.Fatalf("unexpected status bar left: %q", cleanLeft)
	}
	if !strings.Contains(cleanLeft, "42.5 t/s") {
		t.Fatalf("expected TPS in status bar: %q", cleanLeft)
	}

	right := FormatStatusBarRight(state, theme, 100)
	cleanRight := style.StripAnsi(right)
	if !strings.Contains(cleanRight, "gpt-4o") {
		t.Fatalf("expected model in status bar right: %q", cleanRight)
	}

	line := FormatStatusBarLine(state, theme, 100)
	cleanLine := style.StripAnsi(line)
	if !strings.Contains(cleanLine, "1.5k in") || !strings.Contains(cleanLine, "gpt-4o") {
		t.Fatalf("unexpected status bar line: %q", cleanLine)
	}

	sep := FormatStatusBarSeparator(80, "▾", theme)
	cleanSep := style.StripAnsi(sep)
	if !strings.Contains(cleanSep, "▾") || !strings.Contains(cleanSep, "─") {
		t.Fatalf("unexpected separator: %q", cleanSep)
	}
}

func TestHighlightWithoutTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	err := HighlightWithoutTrailingNewline(&buf, "const x = 1;", "javascript", "friendly")
	if err != nil {
		t.Fatalf("syntax highlighting failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "const") || !strings.Contains(out, "1") {
		t.Fatalf("unexpected highlighted output: %q", out)
	}
	if strings.HasSuffix(out, "\n") {
		t.Fatalf("single line highlighting should not have trailing newline: %q", out)
	}
}

func TestMarkdownRenderingAndWrapping(t *testing.T) {
	isNum, prefix, text := IsNumberedList("1. First item")
	if !isNum || prefix != "1." || text != "First item" {
		t.Fatalf("unexpected numbered list parsing: %v, %q, %q", isNum, prefix, text)
	}

	wrapped := WrapMarkdownLine("- This is a very long line that should definitely wrap across multiple lines when width is small", 30)
	if len(wrapped) < 2 {
		t.Fatalf("expected line to wrap into >= 2 lines, got %d", len(wrapped))
	}

	theme := style.GetTheme("catppuccin")
	inline := RenderInlineMarkdown("Check `code` and **bold** and *italic*", false, theme)
	cleanInline := style.StripAnsi(inline)
	if cleanInline != "Check code and bold and italic" {
		t.Fatalf("unexpected inline markdown result: %q", cleanInline)
	}

	var buf bytes.Buffer
	RenderMarkdownContent(&buf, "# Title\n- Bullet 1\n```go\nfmt.Println(1)\n```\n", theme)
	cleanMd := style.StripAnsi(buf.String())
	if !strings.Contains(cleanMd, "Title") || !strings.Contains(cleanMd, "Bullet 1") || !strings.Contains(cleanMd, "fmt.Println(1)") {
		t.Fatalf("unexpected markdown content: %q", cleanMd)
	}
}

func TestPromptSeparatorAndStatsLine(t *testing.T) {
	theme := style.GetTheme("catppuccin")
	sep := FormatPromptSeparator(true, "high", theme, 80)
	cleanSep := style.StripAnsi(sep)
	if !strings.Contains(cleanSep, "prompt") || !strings.Contains(cleanSep, "[reasoning:high]") {
		t.Fatalf("unexpected prompt separator: %q", cleanSep)
	}

	stats := FormatStaticStatsContent("⠋", "120 tokens", theme)
	cleanStats := style.StripAnsi(stats)
	if !strings.Contains(cleanStats, "⠋") || !strings.Contains(cleanStats, "120 tokens") {
		t.Fatalf("unexpected stats content: %q", cleanStats)
	}
}
