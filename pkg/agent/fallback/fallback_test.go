package fallback_test

import (
	"fmt"
	"strings"
	"testing"

	"loop/pkg/agent/fallback"
)

func filterFallbackChunks(chunks ...string) string {
	var output strings.Builder
	filter := fallback.NewToolTextFilter(func(text string) {
		output.WriteString(text)
	})
	for _, chunk := range chunks {
		filter.Write(chunk)
	}
	filter.Flush()
	return output.String()
}

func TestFallbackToolTextFilterHandlesEveryChunkBoundary(t *testing.T) {
	input := "Before\n" +
		`<tool_call name="read">{"path":"a.py"}</tool_call>` + "\n" +
		`<tool:read>{"path":"b.py"}</tool:read>` + "\n" +
		`<execute name="read">{"path":"c.py"}</execute>` + "\n" +
		"After"
	const expected = "Before\nAfter"

	for split := 0; split <= len(input); split++ {
		got := filterFallbackChunks(input[:split], input[split:])
		if got != expected {
			t.Fatalf("split %d produced %q, want %q", split, got, expected)
		}
	}
}

func TestParseFallbackToolCalls(t *testing.T) {
	input := `I'll create the file now.
<tool_call name="write">{"path": "foo.txt", "content": "bar"}</tool_call>
And run tests:
<tool:bash>{"command": "go test ./..."}</tool:bash>`

	calls := fallback.ParseFallbackToolCalls(input)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Function.Name != "write" {
		t.Errorf("expected write, got %s", calls[0].Function.Name)
	}
	if calls[1].Function.Name != "bash" {
		t.Errorf("expected bash, got %s", calls[1].Function.Name)
	}
}

func TestParseFallbackToolCallsBareFunctionDialect(t *testing.T) {
	bare := "<function=bash\n<parametercommand\nls -la /tmp\n</parameter\n</function>"
	calls := fallback.ParseFallbackToolCalls(bare)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call from bare function dialect, got %d", len(calls))
	}
	if calls[0].Function.Name != "bash" {
		t.Fatalf("expected tool 'bash', got %q", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "ls -la /tmp") {
		t.Fatalf("expected command argument, got %q", calls[0].Function.Arguments)
	}
}

func TestFallbackToolTextFilterEmitsToolCallbacks(t *testing.T) {
	var names []string
	var calls []string
	filter := fallback.NewToolTextFilter(func(text string) {})
	filter.SetToolCallbacks(
		func(toolName string, idx int) {
			names = append(names, fmt.Sprintf("%d:%s", idx, toolName))
		},
		func(chunk string, idx int) {
			calls = append(calls, chunk)
		},
	)

	chunks := []string{
		"I'll write the file now.\n<tool_call name=\"write\">",
		`{"path": "hello.txt", `,
		`"content": "hello world"}`,
		"</tool_call>\nAll done!",
	}
	for _, c := range chunks {
		filter.Write(c)
	}
	filter.Flush()

	if len(names) != 1 || names[0] != "0:write" {
		t.Fatalf("expected tool name '0:write', got: %v", names)
	}
	fullBody := strings.Join(calls, "")
	if !strings.Contains(fullBody, `"path": "hello.txt"`) || !strings.Contains(fullBody, `"content": "hello world"`) {
		t.Fatalf("expected streamed tool call chunks, got: %q", fullBody)
	}
}

func TestParseFallbackToolCallsXMLDirectTags(t *testing.T) {
	input := `I will create App.vue now:
<write path="frontend/src/App.vue">
<script>
console.log("hello");
</script>
</write>
And edit model.py:
<edit path="model.py">
<old>val = 1</old>
<new>val = 2</new>
</edit>
And check git:
<bash>
git status
</bash>
And inspect readme:
<read path="README.md" />`

	calls := fallback.ParseFallbackToolCalls(input)
	if len(calls) != 4 {
		t.Fatalf("expected 4 XML tool calls, got %d", len(calls))
	}

	// Verify write
	if calls[0].Function.Name != "write" {
		t.Fatalf("call 0: expected write, got %s", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, `"path":"frontend/src/App.vue"`) {
		t.Errorf("call 0: expected path in arguments, got: %s", calls[0].Function.Arguments)
	}
	if !strings.Contains(calls[0].Function.Arguments, `console.log(\"hello\");`) {
		t.Errorf("call 0: expected content in arguments, got: %s", calls[0].Function.Arguments)
	}

	// Verify edit
	if calls[1].Function.Name != "edit" {
		t.Fatalf("call 1: expected edit, got %s", calls[1].Function.Name)
	}
	if !strings.Contains(calls[1].Function.Arguments, `"oldText":"val = 1"`) || !strings.Contains(calls[1].Function.Arguments, `"newText":"val = 2"`) {
		t.Errorf("call 1: expected updates in arguments, got: %s", calls[1].Function.Arguments)
	}

	// Verify bash
	if calls[2].Function.Name != "bash" {
		t.Fatalf("call 2: expected bash, got %s", calls[2].Function.Name)
	}
	if !strings.Contains(calls[2].Function.Arguments, "git status") {
		t.Errorf("call 2: expected git status command, got: %s", calls[2].Function.Arguments)
	}

	// Verify read
	if calls[3].Function.Name != "read" {
		t.Fatalf("call 3: expected read, got %s", calls[3].Function.Name)
	}
	if !strings.Contains(calls[3].Function.Arguments, `"path":"README.md"`) {
		t.Errorf("call 3: expected path README.md, got: %s", calls[3].Function.Arguments)
	}
}

func TestFallbackToolTextFilterXMLTargetCallback(t *testing.T) {
	var names []string
	var targets []string
	var calls []string
	filter := fallback.NewToolTextFilter(func(text string) {})
	filter.SetToolCallbacks(
		func(toolName string, idx int) {
			names = append(names, fmt.Sprintf("%d:%s", idx, toolName))
		},
		func(chunk string, idx int) {
			calls = append(calls, chunk)
		},
	)
	filter.SetToolTargetCallback(func(target string, idx int) {
		targets = append(targets, fmt.Sprintf("%d:%s", idx, target))
	})

	chunks := []string{
		"Creating component:\n<write path=\"src/App.vue\">\n",
		"<template><div>hello</div></template>\n",
		"</write>\nDone!",
	}
	for _, c := range chunks {
		filter.Write(c)
	}
	filter.Flush()

	if len(names) != 1 || names[0] != "0:write" {
		t.Fatalf("expected tool name '0:write', got: %v", names)
	}
	if len(targets) != 1 || targets[0] != "0:src/App.vue" {
		t.Fatalf("expected tool target '0:src/App.vue', got: %v", targets)
	}
	fullBody := strings.Join(calls, "")
	if !strings.Contains(fullBody, "<template><div>hello</div></template>") {
		t.Fatalf("expected body streamed cleanly, got: %q", fullBody)
	}
}
