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

	if glyph := GetToolGlyph("todo"); glyph != "≡" {
		t.Fatalf("expected ≡ for todo, got %s", glyph)
	}
	if glyph := GetToolGlyph("ask_user"); glyph != "?" {
		t.Fatalf("expected ? for ask_user, got %s", glyph)
	}

	askTarget := ExtractToolTarget("ask_user", `{"question": "Proceed with deployment?"}`)
	if askTarget != "Proceed with deployment?" {
		t.Fatalf("expected question target for ask_user, got: %s", askTarget)
	}

	todoTarget := ExtractToolTarget("todo", `{"tasks": [{"id": "1", "task": "Build backend", "status": "in_progress"}]}`)
	if todoTarget != "Build backend" {
		t.Fatalf("expected active task target for todo, got: %s", todoTarget)
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

func TestDiffStatsAndInstructions(t *testing.T) {
	theme := style.GetTheme("catppuccin")

	// 1. Test CountDiffLines
	diffWithAnsi := "\x1b[31m1    - old line 1\x1b[0m\n\x1b[31m2    - old line 2\x1b[0m\n\x1b[32m1    + new line 1\x1b[0m\n\x1b[32m2    + new line 2\x1b[0m\n\x1b[32m3    + new line 3\x1b[0m\n4      context line\n     ...\n"
	added, deleted := CountDiffLines(diffWithAnsi)
	if added != 3 || deleted != 2 {
		t.Fatalf("expected 3 added and 2 deleted, got added=%d, deleted=%d", added, deleted)
	}

	// Plain diff without ANSI
	plainDiff := "1    - old line\n1    + new line\n"
	added, deleted = CountDiffLines(plainDiff)
	if added != 1 || deleted != 1 {
		t.Fatalf("expected 1 added and 1 deleted, got added=%d, deleted=%d", added, deleted)
	}

	// 2. Test FormatDiffStats colors
	stats := FormatDiffStats(added, deleted, theme)
	if !strings.Contains(stats, "+1") || !strings.Contains(stats, "-1") || !strings.Contains(stats, "/") || !strings.Contains(stats, "lines") {
		t.Fatalf("expected formatted stats to contain '+1 / -1 lines', got: %q", stats)
	}

	// 3. Test ExtractToolInstructions
	argsWithInstructions := `{"path":"main.go","instructions":"refactor server setup","updates":[{"oldText":"a","newText":"b"}]}`
	instr := ExtractToolInstructions(argsWithInstructions)
	if instr != "refactor server setup" {
		t.Fatalf("expected 'refactor server setup', got %q", instr)
	}

	argsWithInstruction := `{"path":"main.go","instruction":"fix bug"}`
	instr2 := ExtractToolInstructions(argsWithInstruction)
	if instr2 != "fix bug" {
		t.Fatalf("expected 'fix bug', got %q", instr2)
	}

	// 4. Test RenderToolOutput for edit produces concise summary on front only
	var buf bytes.Buffer
	RenderToolOutput(&buf, diffWithAnsi, false, false, theme, "edit", argsWithInstructions, false)
	rendered := buf.String()

	if !strings.Contains(rendered, "main.go") {
		t.Fatalf("expected rendered edit output to contain filename, got: %s", rendered)
	}
	if !strings.Contains(rendered, "+3") || !strings.Contains(rendered, "-2") || !strings.Contains(rendered, "lines") {
		t.Fatalf("expected rendered edit output to contain '+3 / -2 lines', got: %s", rendered)
	}
	if !strings.Contains(rendered, "new line 1") || !strings.Contains(rendered, "old line 1") {
		t.Fatalf("expected rendered edit output to contain diff lines, got: %s", rendered)
	}
	if !strings.Contains(rendered, "⎿") {
		t.Fatalf("expected rendered edit output to contain '⎿', got: %s", rendered)
	}
	if strings.Contains(rendered, "✓") {
		t.Fatalf("expected rendered edit output to not contain '✓', got: %s", rendered)
	}
	firstLine := strings.Split(style.StripAnsi(rendered), "\n")[0]
	if !strings.HasPrefix(firstLine, "⎿  ") {
		t.Fatalf("expected edit summary to start with '⎿  ', got: %q", firstLine)
	}

	// 5. Test RenderToolOutput for write produces summary with ⎿
	var writeBuf bytes.Buffer
	RenderToolOutput(&writeBuf, "wrote file", false, false, theme, "write", `{"path":"main.go"}`, false)
	writeRendered := writeBuf.String()
	if !strings.Contains(writeRendered, "⎿") {
		t.Fatalf("expected rendered write output to contain '⎿', got: %s", writeRendered)
	}
	writeFirstLine := strings.Split(style.StripAnsi(writeRendered), "\n")[0]
	if !strings.HasPrefix(writeFirstLine, "⎿  ") {
		t.Fatalf("expected write summary to start with '⎿  ', got: %q", writeFirstLine)
	}

	// Test RenderToolOutput for error does not have extra leading newline and starts with ⎿
	var errBuf bytes.Buffer
	RenderToolOutput(&errBuf, "failed to execute", true, false, theme, "edit", argsWithInstructions, false)
	errRendered := errBuf.String()
	strippedErr := style.StripAnsi(errRendered)
	if strings.HasPrefix(strippedErr, "\n") {
		t.Fatalf("expected error output not to start with newline supplement, got: %q", strippedErr)
	}
	errFirstLine := strings.Split(strippedErr, "\n")[0]
	if !strings.HasPrefix(errFirstLine, "⎿  ! Error") {
		t.Fatalf("expected error output first line to be '⎿  ! Error', got: %q", errFirstLine)
	}

	// 6. Test ExtractToolTarget for task tools
	taskTarget := ExtractToolTarget("task_status", `{"task_id":"task_42"}`)
	if taskTarget != "task_42" {
		t.Fatalf("expected ExtractToolTarget for task_status to return 'task_42', got: %q", taskTarget)
	}
	taskKillTarget := ExtractToolTarget("task_kill", `{"taskId":"task_99"}`)
	if taskKillTarget != "task_99" {
		t.Fatalf("expected ExtractToolTarget for task_kill to return 'task_99', got: %q", taskKillTarget)
	}

	// 7. Test RenderToolOutput for task_status and task_kill
	var statusBuf bytes.Buffer
	taskStatusOutput := "Task task_1 (echo tick) is currently: running\n\nOutput:\ntick 1\ntick 2"
	RenderToolOutput(&statusBuf, taskStatusOutput, false, false, theme, "task_status", `{"task_id":"task_1"}`, false)
	renderedStatus := statusBuf.String()
	if !strings.Contains(renderedStatus, "⎿  ") || !strings.Contains(renderedStatus, "task_1") || !strings.Contains(renderedStatus, "running") {
		t.Fatalf("expected rendered task_status to contain '⎿  ', 'task_1', and 'running', got: %s", renderedStatus)
	}
	if !strings.Contains(renderedStatus, "tick 1") {
		t.Fatalf("expected rendered task_status to contain output 'tick 1', got: %s", renderedStatus)
	}

	var killBuf bytes.Buffer
	RenderToolOutput(&killBuf, "Task task_1 successfully terminated.", false, false, theme, "task_kill", `{"task_id":"task_1"}`, false)
	renderedKill := killBuf.String()
	if !strings.Contains(renderedKill, "⎿  ") || !strings.Contains(renderedKill, "task_1") || !strings.Contains(renderedKill, "terminated") {
		t.Fatalf("expected rendered task_kill to contain '⎿  ', 'task_1', and 'terminated', got: %s", renderedKill)
	}
}
