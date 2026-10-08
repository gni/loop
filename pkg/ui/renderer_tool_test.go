package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type renderLineCounter struct {
	bytes.Buffer
	count int
}

func (w *renderLineCounter) Write(p []byte) (int, error) {
	w.count += bytes.Count(p, []byte{'\n'})
	return w.Buffer.Write(p)
}

func (w *renderLineCounter) GetCount() int {
	return w.count
}

type wrappedRenderLineCounter struct {
	writer io.Writer
	count  int
}

func (w *wrappedRenderLineCounter) Write(p []byte) (int, error) {
	w.count += bytes.Count(p, []byte{'\n'})
	return w.writer.Write(p)
}

func (w *wrappedRenderLineCounter) GetCount() int {
	return w.count
}

func (w *wrappedRenderLineCounter) Unwrap() io.Writer {
	return w.writer
}

func TestStreamedToolTargetUsesOneStableHeader(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(`{"path": "README.md"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	if count := strings.Count(rendered, "README.md"); count != 1 {
		t.Fatalf("expected target once, got %d occurrences in %q", count, rendered)
	}
	if strings.Contains(rendered, "▸ path:") {
		t.Fatalf("target should be represented by the tool header, got %q", rendered)
	}
	if line := renderer.GetToolTitleLineNumber(0); line < 0 {
		t.Fatalf("streamed tool header was not tracked, got line %d", line)
	}
}

func TestThoughtCompletesBeforeToolHeader(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.StartToolCall("create_subagent", 0)
	renderer.WriteToolCall(`{"name":"devops_audit"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIndex := strings.Index(rendered, "thought (")
	toolIndex := strings.Index(rendered, "create_subagent")
	if thoughtIndex < 0 || toolIndex < 0 || !strings.Contains(rendered, "devops_audit") {
		t.Fatalf("missing thought completion or tool header: %q", rendered)
	}
	if thoughtIndex > toolIndex {
		t.Fatalf("thought completion rendered after its tool call: %q", rendered)
	}
	between := rendered[thoughtIndex:toolIndex]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("thought and tool header must have exactly one blank row: %q", rendered)
	}
}

func TestThoughtBashToolSpacing(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("inspecting pytest warnings")
	renderer.StartToolCall("bash", 0)
	renderer.WriteToolCall(`{"command":"cd /home/w/conq && grep -n \"^def test\" tests/test_api.py"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIdx := strings.Index(rendered, "thought (")
	bashIdx := strings.Index(rendered, "$ cd /home/w/conq")
	if thoughtIdx < 0 || bashIdx < 0 || thoughtIdx > bashIdx {
		t.Fatalf("missing ordered thought and bash command: %q", rendered)
	}
	between := rendered[thoughtIdx:bashIdx]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("thought and bash tool run must have exactly one blank row: %q", rendered)
	}
}

func TestThoughtTextToolSpacing(t *testing.T) {
	const (
		reasoning = "checking CI dependencies"
		text      = "Let me check the CI workflow first, then install deps so tests can actually run."
		command   = "cd /home/w/conq && cat .github/workflows/ci.yml; .venv/bin/python --version"
	)
	theme := UITheme{}
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, theme, true, false, "test")

	renderer.WriteReasoning(reasoning)
	renderer.Write(text)
	renderer.StartToolCall("bash", 0)
	renderer.WriteToolCall(fmt.Sprintf(`{"command":%q}`, command))
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIdx := strings.Index(rendered, "thought (")
	textIdx := strings.Index(rendered, text)
	bashIdx := strings.Index(rendered, "$ cd /home/w/conq")
	if thoughtIdx < 0 || textIdx < 0 || bashIdx < 0 {
		t.Fatalf("missing parts in output: %q", rendered)
	}
	if !(thoughtIdx < textIdx && textIdx < bashIdx) {
		t.Fatalf("incorrect order: thought=%d, text=%d, bash=%d", thoughtIdx, textIdx, bashIdx)
	}

	betweenThoughtAndText := rendered[thoughtIdx:textIdx]
	if !strings.Contains(betweenThoughtAndText, "\n\n") || strings.Contains(betweenThoughtAndText, "\n\n\n") {
		t.Fatalf("thought and text must have exactly one blank row: %q", betweenThoughtAndText)
	}

	betweenTextAndBash := rendered[textIdx+len(text) : bashIdx]
	if !strings.Contains(betweenTextAndBash, "\n\n") || strings.Contains(betweenTextAndBash, "\n\n\n") {
		t.Fatalf("text and bash tool run must have exactly one blank row: %q", betweenTextAndBash)
	}

	// Verify parity with PrintSessionHistory
	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "bash",
			Arguments: fmt.Sprintf(`{"command":%q}`, command),
		},
	}
	messages := []db.Message{
		{
			Role:              "assistant",
			ReasoningContent:  reasoning,
			ReasoningDuration: renderer.GetReasoningDuration(),
			Content:           text,
			ToolCalls:         []db.ToolCall{toolCall},
		},
		{
			Role:       "tool",
			ToolCallID: toolCall.ID,
			Name:       toolCall.Function.Name,
			Content:    "name: ci",
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, theme, &config.Config{ShowThinking: true})

	historyText := stripAnsi(history.String())
	if !strings.Contains(historyText, "Let me check the CI workflow") || !strings.Contains(historyText, "$ cd /home/w/conq") {
		t.Fatalf("history missing text or bash command: %q", historyText)
	}
	prefixIdx := strings.Index(historyText, "Let me check the CI workflow")
	bashPos := strings.Index(historyText, "$ cd /home/w/conq")
	histBetween := historyText[prefixIdx:bashPos]
	if !strings.Contains(histBetween, "\n\n") || strings.Contains(histBetween, "\n\n\n") {
		t.Fatalf("history text and bash tool must have exactly one blank row: %q", histBetween)
	}
}

func TestTextToolSpacingWithoutThinking(t *testing.T) {
	const (
		text    = "Let me check the CI workflow first, then install deps so tests can actually run."
		command = "cd /home/w/conq && cat .github/workflows/ci.yml; .venv/bin/python --version"
	)
	theme := UITheme{}
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, theme, false, false, "test")

	renderer.Write(text)
	renderer.StartToolCall("bash", 0)
	renderer.WriteToolCall(fmt.Sprintf(`{"command":%q}`, command))
	renderer.Flush()

	rendered := stripAnsi(output.String())
	textIdx := strings.Index(rendered, text)
	bashIdx := strings.Index(rendered, "$ cd /home/w/conq")
	if textIdx < 0 || bashIdx < 0 || textIdx > bashIdx {
		t.Fatalf("missing ordered text and bash command without thinking: %q", rendered)
	}
	between := rendered[textIdx+len(text) : bashIdx]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("text and bash tool run without thinking must have exactly one blank row: %q", between)
	}

	// Verify parity with PrintSessionHistory
	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "bash",
			Arguments: fmt.Sprintf(`{"command":%q}`, command),
		},
	}
	messages := []db.Message{
		{
			Role:      "assistant",
			Content:   text,
			ToolCalls: []db.ToolCall{toolCall},
		},
		{
			Role:       "tool",
			ToolCallID: toolCall.ID,
			Name:       toolCall.Function.Name,
			Content:    "name: ci",
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, theme, &config.Config{ShowThinking: false})

	historyText := stripAnsi(history.String())
	if !strings.Contains(historyText, "Let me check the CI workflow") || !strings.Contains(historyText, "$ cd /home/w/conq") {
		t.Fatalf("history missing text or bash command: %q", historyText)
	}
	hPrefixIdx := strings.Index(historyText, "Let me check the CI workflow")
	hBashPos := strings.Index(historyText, "$ cd /home/w/conq")
	histBetween := historyText[hPrefixIdx:hBashPos]
	if !strings.Contains(histBetween, "\n\n") || strings.Contains(histBetween, "\n\n\n") {
		t.Fatalf("history text and bash tool without thinking must have exactly one blank row: %q", histBetween)
	}
}

func TestUnrespondedToolCallSpacing(t *testing.T) {
	const (
		text    = "Let me check the CI workflow first."
		command = "cat .github/workflows/ci.yml"
	)
	theme := UITheme{}
	toolCall := db.ToolCall{
		ID:   "call-unresp",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "bash",
			Arguments: fmt.Sprintf(`{"command":%q}`, command),
		},
	}
	messages := []db.Message{
		{
			Role:      "assistant",
			Content:   text,
			ToolCalls: []db.ToolCall{toolCall},
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, theme, &config.Config{ShowThinking: false})

	historyText := stripAnsi(history.String())
	textIdx := strings.Index(historyText, text)
	bashIdx := strings.Index(historyText, "$ cat .github/workflows/ci.yml")
	if textIdx < 0 || bashIdx < 0 || textIdx > bashIdx {
		t.Fatalf("missing text or bash header: %q", historyText)
	}
	between := historyText[textIdx+len(text) : bashIdx]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("unresponded tool call must have exactly one blank row after text: %q", between)
	}
}

func TestThoughtUsesOneBlankRowBeforeAnswerText(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.Write("final answer")
	renderer.Flush()

	rendered := stripAnsi(output.String())
	thoughtIndex := strings.Index(rendered, "thought (")
	answerIndex := strings.Index(rendered, "final answer")
	if thoughtIndex < 0 || answerIndex < 0 || thoughtIndex > answerIndex {
		t.Fatalf("missing ordered thought and answer: %q", rendered)
	}
	between := rendered[thoughtIndex:answerIndex]
	if !strings.Contains(between, "\n\n") || strings.Contains(between, "\n\n\n") {
		t.Fatalf("thought and answer must have exactly one blank row: %q", rendered)
	}
}

func TestCompletedThoughtHasNoTransientTrailingBlankRow(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, true, false, "test")

	renderer.WriteReasoning("checking prerequisites")
	renderer.EndThinking()

	rendered := stripAnsi(output.String())
	if strings.HasSuffix(rendered, "\n\n") {
		t.Fatalf("completed thought retained a transient trailing blank row: %q", rendered)
	}
}

func TestLiveThoughtToolLayoutMatchesSessionHistory(t *testing.T) {
	const (
		reasoning  = "Reading `test_security.py`."
		arguments  = `{"path":"test_security.py"}`
		toolOutput = "def test_authentication():\n    pass"
	)
	theme := UITheme{}

	var live renderLineCounter
	fmt.Fprintln(&live, strings.Repeat("╌", 40))
	renderer := NewStreamRenderer(&live, theme, true, false, "test")
	renderer.WriteReasoning(reasoning)
	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	RenderToolOutput(&live, toolOutput, false, false, theme, "read", arguments, renderer.DidStreamToolBody(0))

	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "read",
			Arguments: arguments,
		},
	}
	messages := []db.Message{
		{
			Role:              "assistant",
			ReasoningContent:  reasoning,
			ReasoningDuration: renderer.GetReasoningDuration(),
			ToolCalls:         []db.ToolCall{toolCall},
		},
		{
			Role:       "tool",
			ToolCallID: toolCall.ID,
			Name:       toolCall.Function.Name,
			Content:    toolOutput,
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, theme, &config.Config{ShowThinking: true})

	liveText := stripAnsi(live.String())
	historyText := stripAnsi(history.String())
	if liveText != historyText {
		t.Fatalf("live and persisted layouts differ:\nlive:    %q\nhistory: %q", liveText, historyText)
	}
}

func TestSessionHistoryUsesFinalToolStatus(t *testing.T) {
	toolCall := db.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: db.ToolFunction{
			Name:      "read",
			Arguments: `{"path":"test_security.py"}`,
		},
	}
	messages := []db.Message{
		{Role: "assistant", ToolCalls: []db.ToolCall{toolCall}},
		{Role: "tool", ToolCallID: toolCall.ID, Name: "read", Content: "ok"},
	}

	var output bytes.Buffer
	PrintSessionHistory(&output, messages, UITheme{}, &config.Config{})

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, "read test_security.py") || strings.Contains(rendered, "───") {
		t.Fatalf("persisted successful tool header did not use expected format: %q", rendered)
	}
}

func TestSuccessfulReadOutputFollowsHeaderDirectly(t *testing.T) {
	var output bytes.Buffer
	arguments := `{"path":"ssl_checker/app/api/v1/api.py"}`

	RenderToolHeader(&output, UITheme{}, "read", arguments)
	RenderToolOutput(&output, "from fastapi import APIRouter", false, false, UITheme{}, "read", arguments, false)

	rendered := stripAnsi(output.String())
	if strings.Contains(rendered, "Output") {
		t.Fatalf("read result included a redundant output label: %q", rendered)
	}
	if strings.Contains(rendered, "api.py\n\nfrom fastapi") {
		t.Fatalf("read result included an extra blank line after its header: %q", rendered)
	}
	if !strings.Contains(rendered, "\nfrom fastapi") {
		t.Fatalf("read content did not follow its header directly: %q", rendered)
	}
}

func TestSuccessfulBashOutputFollowsHeaderDirectly(t *testing.T) {
	var output bytes.Buffer
	arguments := `{"command":"find . -maxdepth 3"}`

	RenderToolHeader(&output, UITheme{}, "bash", arguments)
	RenderToolOutput(&output, "./app\n./tests", false, false, UITheme{}, "bash", arguments, false)

	rendered := stripAnsi(output.String())
	if strings.Contains(rendered, "Output") {
		t.Fatalf("bash result included a redundant output label: %q", rendered)
	}
	if strings.Contains(rendered, "\n\n./app") {
		t.Fatalf("bash result included an extra blank line after its header: %q", rendered)
	}
	if !strings.Contains(rendered, "\n./app") {
		t.Fatalf("bash content did not follow its header directly: %q", rendered)
	}
}

func TestCollapsedResultsNeverHideToolErrors(t *testing.T) {
	var output bytes.Buffer
	const diagnostic = "npm ERR! code ERESOLVE\nnpm ERR! unable to resolve dependency tree"

	RenderToolOutput(
		&output,
		diagnostic,
		true,
		true,
		UITheme{},
		"bash",
		`{"command":"npm install"}`,
		false,
	)

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, diagnostic) {
		t.Fatalf("collapsed results hid the command diagnostic: %q", rendered)
	}
	if strings.Contains(rendered, "lines collapsed") {
		t.Fatalf("tool error was replaced by a collapsed placeholder: %q", rendered)
	}
}

func TestSequentialStreamedToolsKeepIndependentHeaders(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(`{"path":"README.md"}`)
	renderer.StartToolCall("load_skill", 1)
	renderer.WriteToolCall(`{"name":"loop-brain"}`)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	for _, target := range []string{"README.md", "loop-brain"} {
		if count := strings.Count(rendered, target); count != 1 {
			t.Fatalf("expected %q once, got %d occurrences in %q", target, count, rendered)
		}
	}
	for index := 0; index < 2; index++ {
		if line := renderer.GetToolTitleLineNumber(index); line < 0 {
			t.Fatalf("tool %d has no independently tracked header", index)
		}
	}
}

func TestStreamedWriteContentIsNotRenderedTwice(t *testing.T) {
	const payload = "unique streamed payload"

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
	arguments := `{"path":"result.txt","content":"unique streamed payload"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	RenderToolOutput(&output, "wrote result.txt", false, false, UITheme{}, "write", arguments, true)

	rendered := stripAnsi(output.String())
	if count := strings.Count(rendered, payload); count != 1 {
		t.Fatalf("expected streamed write content once, got %d occurrences in %q", count, rendered)
	}
	if strings.Contains(rendered, "wrote result.txt") {
		t.Fatalf("expected redundant post-stream write output to be suppressed, got %q", rendered)
	}
}

func TestStreamedWriteContentBeforePathIncludesFilename(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
	arguments := `{"write_content":"import sys\nimport os\n","path":"nested/tool.py"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, "write nested/tool.py") {
		t.Fatalf("expected header 'write nested/tool.py', got %q", rendered)
	}
	if !strings.Contains(rendered, "import sys") {
		t.Fatalf("expected streamed content 'import sys', got %q", rendered)
	}
	headerIdx := strings.Index(rendered, "write nested/tool.py")
	codeIdx := strings.Index(rendered, "import sys")
	if headerIdx > codeIdx {
		t.Fatalf("expected header to appear before code, got header at %d, code at %d", headerIdx, codeIdx)
	}
}

func TestStreamedWriteContentBeforePathDoesNotCorruptAnsiCodes(t *testing.T) {
	var output renderLineCounter
	theme := UITheme{ChromaStyle: "dracula"}
	renderer := NewStreamRenderer(&output, theme, false, true, "test")
	arguments := `{"content":"from __future__ import annotations\nimport os\n","path":"petitbleu/src/main.py"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	raw := output.String()
	if strings.Contains(raw, "[97m") && !strings.Contains(raw, "\x1b[97m") {
		t.Fatalf("raw [97m escape leaked into output: %q", raw)
	}
	if strings.Contains(raw, "[0m") && !strings.Contains(raw, "\x1b[0m") {
		t.Fatalf("raw [0m escape leaked into output: %q", raw)
	}

	rendered := stripAnsi(raw)
	if !strings.Contains(rendered, "from __future__ import annotations") {
		t.Fatalf("expected code line in output, got: %q", rendered)
	}
}

func TestStreamedWriteAlternativePathKeys(t *testing.T) {
	for _, key := range []string{"file_path", "filePath", "file", "target", "filename"} {
		var output renderLineCounter
		renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
		arguments := fmt.Sprintf(`{"%s":"service/api.py","write_content":"from flask import Flask\n"}`, key)

		renderer.StartToolCall("write", 0)
		renderer.WriteToolCall(arguments)
		renderer.Flush()

		rendered := stripAnsi(output.String())
		if !strings.Contains(rendered, "write service/api.py") {
			t.Fatalf("key %q: expected header 'write service/api.py', got %q", key, rendered)
		}
		if !strings.Contains(rendered, "from flask import Flask") {
			t.Fatalf("key %q: expected streamed content, got %q", key, rendered)
		}
	}
}

func TestEditIntentIsNotRenderedBeforeExecution(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")

	renderer.StartToolCall("edit", 0)
	for _, chunk := range []string{
		`{"path":"certificate.py","oldText":"from datetime import datetime\n`,
		`value = 1","newText":"from datetime import datetime, timezone\n`,
		`value = 2"}`,
	} {
		renderer.WriteToolCall(chunk)
	}
	renderer.Flush()

	rendered := sanitizeTerminalText(output.String())
	for _, hiddenIntent := range []string{
		"from datetime import datetime",
		"value = 1",
		"from datetime import datetime, timezone",
		"value = 2",
	} {
		if strings.Contains(rendered, hiddenIntent) {
			t.Fatalf("edit intent appeared before execution: %q in %q", hiddenIntent, rendered)
		}
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("edit intent was marked as an executed tool body")
	}
}

func TestEditDiffIsRenderedOnlyAtCompletion(t *testing.T) {
	const (
		oldLine = "value = before"
		newLine = "value = after"
	)

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
	arguments := `{"path":"model.py","updates":[{"oldText":"value = before","newText":"value = after"}]}`

	renderer.StartToolCall("edit", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	if renderer.DidStreamToolBody(0) {
		t.Fatal("edit intent was reported as streamed output")
	}

	completedDiff := "\x1b[31m1    - value = before\x1b[0m\n\x1b[32m1    + value = after\x1b[0m"
	RenderToolOutput(&output, completedDiff, false, false, UITheme{}, "edit", arguments, renderer.DidStreamToolBody(0))

	rendered := sanitizeTerminalText(output.String())
	for _, line := range []string{"- " + oldLine, "+ " + newLine} {
		if count := strings.Count(rendered, line); count != 1 {
			t.Fatalf("expected edit line %q once across stream and completion, got %d in %q", line, count, rendered)
		}
	}
}

func TestDisabledEditStreamingRendersDiffOnlyAtCompletion(t *testing.T) {
	const (
		oldLine = "value = before"
		newLine = "value = after"
	)

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")
	arguments := `{"path":"model.py","updates":[{"oldText":"value = before","newText":"value = after"}]}`

	renderer.StartToolCall("edit", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	beforeCompletion := sanitizeTerminalText(output.String())
	if strings.Contains(beforeCompletion, oldLine) || strings.Contains(beforeCompletion, newLine) {
		t.Fatalf("edit arguments appeared before completion: %q", beforeCompletion)
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("renderer reported a streamed edit body while streaming was disabled")
	}

	completedDiff := "\x1b[31m1    - value = before\x1b[0m\n\x1b[32m1    + value = after\x1b[0m"
	RenderToolOutput(&output, completedDiff, false, false, UITheme{}, "edit", arguments, renderer.DidStreamToolBody(0))

	rendered := sanitizeTerminalText(output.String())
	for _, line := range []string{"- " + oldLine, "+ " + newLine} {
		if count := strings.Count(rendered, line); count != 1 {
			t.Fatalf("expected final edit line %q once, got %d in %q", line, count, rendered)
		}
	}
}

func TestDisabledWriteStreamingRendersContentOnlyAtCompletion(t *testing.T) {
	const payload = "completion-only payload"

	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")
	arguments := `{"path":"result.txt","content":"completion-only payload"}`

	renderer.StartToolCall("write", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()
	if strings.Contains(stripAnsi(output.String()), payload) {
		t.Fatalf("write content appeared before completion: %q", stripAnsi(output.String()))
	}
	if renderer.DidStreamToolBody(0) {
		t.Fatal("renderer reported a streamed body while write streaming was disabled")
	}

	RenderToolOutput(&output, "wrote result.txt", false, false, UITheme{}, "write", arguments, renderer.DidStreamToolBody(0))
	if count := strings.Count(stripAnsi(output.String()), payload); count != 1 {
		t.Fatalf("expected completion content once, got %d", count)
	}
}

func TestToolCompletionUpdatesOnlyTrackedHeaderRow(t *testing.T) {
	var terminal bytes.Buffer
	promptWriter := NewPromptPreservingWriter(&terminal, 30)
	counter := &wrappedRenderLineCounter{writer: promptWriter}
	renderer := NewStreamRenderer(counter, UITheme{}, false, false, "test")
	arguments := `{"path":"README.md"}`

	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	terminal.Reset()
	renderer.CompleteToolCall(0, "read", arguments, false)

	update := terminal.String()
	if count := strings.Count(update, "\x1b[2K"); count != 1 {
		t.Fatalf("expected one header-row clear, got %d in %q", count, update)
	}
	if strings.Contains(update, "\x1b[J") {
		t.Fatalf("completion must not clear the surrounding screen: %q", update)
	}
	clean := stripAnsi(update)
	if !strings.Contains(clean, "read") || !strings.Contains(clean, "README.md") || strings.Contains(clean, "───") {
		t.Fatalf("completion did not update the tracked title: %q", clean)
	}
}

func TestBashToolHeaderAndCompletionFormat(t *testing.T) {
	var terminal bytes.Buffer
	promptWriter := NewPromptPreservingWriter(&terminal, 30)
	counter := &wrappedRenderLineCounter{writer: promptWriter}
	renderer := NewStreamRenderer(counter, UITheme{}, false, false, "test")
	arguments := `{"command":"whoami"}`

	renderer.StartToolCall("bash", 0)
	renderer.WriteToolCall(arguments)
	renderer.Flush()

	initialOutput := stripAnsi(terminal.String())
	if !strings.Contains(initialOutput, "$ whoami") || strings.Contains(initialOutput, "›") {
		t.Fatalf("expected initial bash command line '$ whoami' without chevron, got: %q", initialOutput)
	}
	if strings.Contains(initialOutput, "───") {
		t.Fatalf("tool call line must not contain horizontal delimiter: %q", initialOutput)
	}

	terminal.Reset()
	renderer.CompleteToolCall(0, "bash", arguments, false)

	update := stripAnsi(terminal.String())
	if !strings.Contains(update, "$ whoami") || strings.Contains(update, "›") {
		t.Fatalf("expected completion bash command line '$ whoami', got: %q", update)
	}
	if strings.Contains(update, "─── $") {
		t.Fatalf("completion must not contain delimiter with bash title: %q", update)
	}

	// Verify session history formatting for bash
	messages := []db.Message{
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:   "call-bash-1",
					Type: "function",
					Function: db.ToolFunction{
						Name:      "bash",
						Arguments: arguments,
					},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call-bash-1",
			Name:       "bash",
			Content:    "node",
		},
	}
	var history bytes.Buffer
	PrintSessionHistory(&history, messages, UITheme{}, &config.Config{})
	historyText := stripAnsi(history.String())

	if !strings.Contains(historyText, "$ whoami") || strings.Contains(historyText, "›") {
		t.Fatalf("expected session history to contain '$ whoami', got: %q", historyText)
	}
	if strings.Contains(historyText, "─── $") {
		t.Fatalf("expected session history delimiter not to contain bash title, got: %q", historyText)
	}
	if !strings.Contains(historyText, "node") {
		t.Fatalf("expected session history to contain output 'node', got: %q", historyText)
	}
}

func TestBashCommandFullVisibilityWithoutTruncation(t *testing.T) {
	theme := UITheme{}

	// 1. Long command exceeding standard 80-col terminal width must not be truncated with "..."
	longCmd := "export PATH=$HOME/go/go/bin:$PATH mkdir -p /tmp/gotest && cd /tmp/gotest && cat > pc.go <<'EOF' package main import (\"fmt\";\"net\";\"os\";\"path/filepath\";\"syscall\")"
	formattedLong := stripAnsi(FormatBashCommandLine("", longCmd, theme))
	if strings.HasSuffix(formattedLong, "...") {
		t.Fatalf("long bash command was truncated with '...': %q", formattedLong)
	}
	if !strings.Contains(formattedLong, "syscall") {
		t.Fatalf("long bash command is missing ending tokens: %q", formattedLong)
	}

	// 2. Multi-line command preserves all lines
	multilineCmd := "export PATH=$HOME/go/go/bin:$PATH\nmkdir -p /tmp/gotest\ncat > pc.go <<'EOF'\npackage main\nEOF"
	formattedMulti := stripAnsi(FormatBashCommandLine("", multilineCmd, theme))
	if !strings.Contains(formattedMulti, "$ export PATH=$HOME/go/go/bin:$PATH") {
		t.Fatalf("first line missing '$ ' prompt: %q", formattedMulti)
	}
	if !strings.Contains(formattedMulti, "  mkdir -p /tmp/gotest") {
		t.Fatalf("second line missing indentation: %q", formattedMulti)
	}
	if !strings.Contains(formattedMulti, "  package main") {
		t.Fatalf("heredoc content line missing: %q", formattedMulti)
	}
	if strings.Contains(formattedMulti, "...") {
		t.Fatalf("multiline command was truncated with '...': %q", formattedMulti)
	}
}

func TestStreamedWriteMultiToolIndex(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")

	// Simulate tool 0 (e.g. todo)
	renderer.StartToolCall("todo", 0)
	renderer.WriteToolCall(`{"action":"read"}`)
	renderer.Flush()

	// Simulate tool 1 (write)
	renderer.StartToolCall("write", 1)
	renderer.WriteToolCall(`{"path":"backend/run_turn.py","content":"def run_turn():\n    pass\n"}`)
	renderer.Flush()

	if !renderer.DidStreamToolBody(1) {
		t.Fatalf("expected tool call index 1 body to be recorded as streamed")
	}

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, "backend/run_turn.py") {
		t.Fatalf("expected write header in output, got: %q", rendered)
	}
	if !strings.Contains(rendered, "def run_turn():") {
		t.Fatalf("expected streamed python content in output, got: %q", rendered)
	}
}

func TestStreamedWriteErrorIsStillRendered(t *testing.T) {
	var output renderLineCounter
	arguments := `{"path":"read_only.txt","content":"some content"}`
	errorMessage := "Error: permission denied"

	RenderToolOutput(&output, errorMessage, true, false, UITheme{}, "write", arguments, true)

	rendered := stripAnsi(output.String())
	if !strings.Contains(rendered, errorMessage) {
		t.Fatalf("expected error message %q to be rendered even when body was streamed, got %q", errorMessage, rendered)
	}
}
func TestHighlightYamlAndMarkdown(t *testing.T) {
	var buf bytes.Buffer
	errYaml := HighlightWithoutTrailingNewline(&buf, "name: test", "yaml", "friendly")
	if errYaml != nil {
		t.Fatalf("yaml highlight failed: %v", errYaml)
	}
	buf.Reset()

	errYml := HighlightWithoutTrailingNewline(&buf, "name: test", "yml", "friendly")
	if errYml != nil {
		t.Fatalf("yml highlight failed: %v", errYml)
	}
	buf.Reset()

	errMd := HighlightWithoutTrailingNewline(&buf, "# header", "md", "friendly")
	if errMd != nil {
		t.Fatalf("md highlight failed: %v", errMd)
	}
	buf.Reset()

	errMarkdown := HighlightWithoutTrailingNewline(&buf, "# header", "markdown", "friendly")
	if errMarkdown != nil {
		t.Fatalf("markdown highlight failed: %v", errMarkdown)
	}
}

func TestStreamedWriteYamlAndMarkdownRealtime(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		content  string
		pathLast bool
	}{
		{
			name:     "yaml path first",
			path:     "docker-compose.yml",
			content:  "version: '3.8'\nservices:\n  app:\n    image: app:latest\n",
			pathLast: false,
		},
		{
			name:     "yaml content first",
			path:     "config.yaml",
			content:  "database:\n  host: localhost\n  port: 5432\n",
			pathLast: true,
		},
		{
			name:     "markdown path first",
			path:     "README.md",
			content:  "# Project Overview\n\nThis is production markdown.\n",
			pathLast: false,
		},
		{
			name:     "markdown content first",
			path:     "docs/architecture.markdown",
			content:  "## Architecture\n- Locality of Behavior\n- Zero-trust\n",
			pathLast: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output renderLineCounter
			renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
			renderer.StartToolCall("write", 0)

			if tc.pathLast {
				renderer.WriteToolCall(fmt.Sprintf(`{"content": %q`, tc.content))
				renderedMid := stripAnsi(output.String())
				if renderedMid != "" {
					t.Fatalf("expected content to buffer until path arrives without emitting bare title, got: %q", renderedMid)
				}
				renderer.WriteToolCall(fmt.Sprintf(`, "path": %q}`, tc.path))
			} else {
				renderer.WriteToolCall(fmt.Sprintf(`{"path": %q, "content": %q}`, tc.path, tc.content))
			}

			renderer.Flush()

			if !renderer.DidStreamToolBody(0) {
				t.Fatalf("expected DidStreamToolBody=true for %s", tc.name)
			}

			rendered := stripAnsi(output.String())
			if !strings.Contains(rendered, fmt.Sprintf("write %s", tc.path)) {
				t.Fatalf("expected header 'write %s', got: %q", tc.path, rendered)
			}
			for _, line := range strings.Split(strings.TrimSpace(tc.content), "\n") {
				if !strings.Contains(rendered, line) {
					t.Fatalf("expected streamed line %q in %q", line, rendered)
				}
			}

			// Post-stream RenderToolOutput must suppress duplicate body
			beforeOutput := output.String()
			RenderToolOutput(&output, "wrote "+tc.path, false, false, UITheme{}, "write", fmt.Sprintf(`{"path":%q}`, tc.path), renderer.DidStreamToolBody(0))
			if output.String() != beforeOutput {
				t.Fatalf("expected RenderToolOutput to be suppressed when body was streamed")
			}
		})
	}
}

func TestStreamedWriteNonCodeContentKeys(t *testing.T) {
	for _, key := range []string{"data", "yaml", "raw", "body", "code", "text", "content"} {
		t.Run("key_"+key, func(t *testing.T) {
			var output renderLineCounter
			renderer := NewStreamRenderer(&output, UITheme{}, false, true, "test")
			renderer.StartToolCall("write", 0)

			args := fmt.Sprintf(`{"path": "config.yml", %q: "app_name: test\nport: 8080\n"}`, key)
			renderer.WriteToolCall(args)
			renderer.Flush()

			if !renderer.DidStreamToolBody(0) {
				t.Fatalf("expected key %q to be recognized as streamed body", key)
			}

			rendered := stripAnsi(output.String())
			if !strings.Contains(rendered, "app_name: test") || !strings.Contains(rendered, "port: 8080") {
				t.Fatalf("expected content streamed for key %q, got: %q", key, rendered)
			}
		})
	}
}

func TestStreamedWriteContentBeforePathSyntaxHighlighting(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{ChromaStyle: "monokai"}, false, true, "test")
	renderer.StartToolCall("write", 0)

	// Stream content chunks first, then path at the end
	renderer.WriteToolCall(`{"content": "import pytest\nfrom fastapi.testclient import TestClient\n`)
	renderer.WriteToolCall(`from sqlalchemy.pool import StaticPool\n", `)
	renderer.WriteToolCall(`"path": "tests/test_api.py"}`)
	renderer.Flush()

	raw := output.String()
	if !strings.Contains(raw, "\x1b[") {
		t.Fatalf("expected syntax highlighting ANSI sequences even when content preceded path, got raw: %q", raw)
	}

	clean := stripAnsi(raw)
	if !strings.Contains(clean, "write tests/test_api.py") {
		t.Fatalf("expected updated title with path, got: %q", clean)
	}
	if !strings.Contains(clean, "import pytest") {
		t.Fatalf("expected content in output, got: %q", clean)
	}
}

func TestStreamedNestedInputPath(t *testing.T) {
	var output renderLineCounter
	renderer := NewStreamRenderer(&output, UITheme{}, false, false, "test")
	renderer.StartToolCall("read", 0)
	renderer.WriteToolCall(`{"input": {"path": "app/security.py"}}`)
	renderer.Flush()

	clean := stripAnsi(output.String())
	if !strings.Contains(clean, "read app/security.py") {
		t.Fatalf("expected nested input path to be recognized in header, got: %q", clean)
	}
}

func TestDetectLangFromContent(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"import pytest\nfrom fastapi import FastAPI", "python"},
		{"from datetime import datetime\nimport jwt", "python"},
		{"def calculate_total(a, b):\n    return a + b", "python"},
		{"package main\n\nfunc main() {}", "go"},
		{"use std::collections::HashMap;\nfn main() {}", "rust"},
		{"import { useState } from 'react';\nexport default App;", "typescript"},
		{"#!/usr/bin/env python3\nprint('hello')", "python"},
		{"#!/bin/bash\nset -euo pipefail", "bash"},
		{"<!DOCTYPE html>\n<html><body></body></html>", "html"},
		{"---\nversion: '3.8'\nservices:", "yaml"},
		{"{\n  \"name\": \"app\"\n}", "json"},
	}

	for _, tc := range cases {
		got := detectLangFromContent(tc.input)
		if got != tc.expected {
			t.Errorf("detectLangFromContent(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestRenderToolSymbolWritePendingColor(t *testing.T) {
	theme := style.GetTheme("tokyonight")

	pendingWrite := renderToolSymbol("write", toolStatusPending, theme)
	successWrite := renderToolSymbol("write", toolStatusSuccess, theme)

	if pendingWrite == successWrite {
		t.Fatalf("expected write pending symbol color to differ from success symbol color, got both: %q", pendingWrite)
	}

	pendingEdit := renderToolSymbol("edit", toolStatusPending, theme)
	successEdit := renderToolSymbol("edit", toolStatusSuccess, theme)

	if pendingEdit == successEdit {
		t.Fatalf("expected edit pending symbol color to differ from success symbol color, got both: %q", pendingEdit)
	}
}

