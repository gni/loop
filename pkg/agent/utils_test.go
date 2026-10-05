package agent

import (
	"errors"
	"strings"
	"testing"

	"loop/pkg/db"
)

func TestFormatDefensiveErrorExplainsOldTextMismatch(t *testing.T) {
	formatted := FormatDefensiveError(
		"edit",
		errors.New("edit[0]: oldText block was not found in file app/models/user.py"),
	)

	if strings.Contains(formatted, "directory structure") {
		t.Fatalf("oldText mismatch was misclassified as a missing path: %q", formatted)
	}
	for _, expected := range []string{"file exists", "current contents", "Read the file again", "Do not recover", "write"} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("oldText mismatch omitted %q: %q", expected, formatted)
		}
	}
}

func TestFormatDefensiveErrorStillExplainsMissingPath(t *testing.T) {
	formatted := FormatDefensiveError("read", errors.New("no such file: missing.py"))
	if !strings.Contains(formatted, "directory structure") {
		t.Fatalf("missing path lost its path-specific recommendation: %q", formatted)
	}
}

func TestFormatDefensiveErrorTaskNotFound(t *testing.T) {
	formatted := FormatDefensiveError("task_status", errors.New("task not found"))
	if strings.Contains(formatted, "ls") || strings.Contains(formatted, "directory structure") {
		t.Fatalf("task error suggested filesystem inspection: %q", formatted)
	}
	if !strings.Contains(formatted, "background task ID was not found") {
		t.Fatalf("task error missing specific guidance: %q", formatted)
	}
}

func TestFormatToolExecutionFailurePreservesCommandDiagnostics(t *testing.T) {
	diagnostic := "npm ERR! code ERESOLVE\nnpm ERR! unable to resolve dependency tree"
	formatted := FormatToolExecutionFailure("bash", diagnostic, errors.New("command failed: exit status 1"))

	for _, expected := range []string{
		"npm ERR! code ERESOLVE",
		"unable to resolve dependency tree",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("tool failure omitted %q: %q", expected, formatted)
		}
	}
	if strings.Contains(formatted, "System Alert:") || strings.Contains(formatted, "Recommendation:") {
		t.Fatalf("bash failure should not contain robotic alert slop: %q", formatted)
	}
}

func TestFormatToolExecutionFailureIncludesDefensiveAlertForGenericTools(t *testing.T) {
	formatted := FormatToolExecutionFailure("read", "failed to read file", errors.New("no such file: missing.py"))
	for _, expected := range []string{
		"failed to read file",
		"System Alert:",
		"Recommendation:",
	} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("generic tool omitted expected alert part %q: %q", expected, formatted)
		}
	}
}

func TestFormatToolExecutionFailureDoesNotRepeatGenericFailure(t *testing.T) {
	err := errors.New("command failed: exit status 1 (no output on stdout or stderr)")
	formatted := FormatToolExecutionFailure("bash", err.Error(), err)

	if count := strings.Count(formatted, err.Error()); count != 1 {
		t.Fatalf("generic failure appeared %d times: %q", count, formatted)
	}
}

func TestFormatToolExecutionFailureBashExplicitErrorHeader(t *testing.T) {
	err := errors.New("command failed: exit status 1")
	traceback := "Traceback (most recent call last):\n  File \"<stdin>\", line 2, in <module>\nNameError: name 'paththlib' is not defined"
	formatted := FormatToolExecutionFailure("bash", traceback, err)

	if !strings.HasPrefix(formatted, "[Command Failed: exit status 1]") {
		t.Fatalf("expected [Command Failed: exit status 1] prefix, got: %q", formatted)
	}
	if !strings.Contains(formatted, "NameError: name 'paththlib' is not defined") {
		t.Fatalf("traceback diagnostics missing from formatted output: %q", formatted)
	}
}

func TestGetGlobalTokensUsesLatestTurnWithoutDoubleCountingPriorCompletions(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first prompt"},
		{Role: "assistant", Content: "first response", PromptTokens: 100, CompletionTokens: 20},
		{Role: "user", Content: "second prompt"},
		{Role: "assistant", Content: "second response", PromptTokens: 160, CompletionTokens: 30},
	}

	prompt, completion := a.GetGlobalTokens(messages, nil)
	if prompt != 160 || completion != 30 {
		t.Fatalf("latest context usage = (%d, %d); want (160, 30)", prompt, completion)
	}

	messages = append(messages, db.Message{Role: "user", Content: "12345678"})
	prompt, completion = a.GetGlobalTokens(messages, nil)
	if prompt != 160 || completion != 30 {
		t.Fatalf("context with pending user message = (%d, %d); want (160, 30)", prompt, completion)
	}
}

func TestGetGlobalTokensIgnoresLegacyEmptyCancellationRecord(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "assistant", Content: "completed response", PromptTokens: 70000, CompletionTokens: 800},
		{Role: "tool", Content: strings.Repeat("x", 100)},
		{Role: "assistant", PromptTokens: 1, CompletionTokens: 1},
	}

	prompt, completion := a.GetGlobalTokens(messages, nil)
	if prompt != 70000 || completion != 800 {
		t.Fatalf("context anchored to empty cancellation record: got (%d, %d), want (70000, 800)", prompt, completion)
	}
}

func TestGetGlobalTokenUsageTreatsProviderMetadataAsMeasured(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hi"},
		{
			Role:             "assistant",
			Content:          "hello",
			PromptTokens:     1600,
			CompletionTokens: 19,
		},
	}

	prompt, completion, estimated := a.GetGlobalTokenUsage(messages, nil)
	if prompt != 1600 || completion != 19 {
		t.Fatalf("provider usage = (%d, %d), want (1600, 19)", prompt, completion)
	}
	if estimated {
		t.Fatal("provider-reported usage was incorrectly marked as estimated")
	}

	messages = append(messages, db.Message{Role: "user", Content: "12345678"})
	prompt, completion, estimated = a.GetGlobalTokenUsage(messages, nil)
	if prompt != 1600 || completion != 19 {
		t.Fatalf("usage with pending input = (%d, %d), want (1600, 19)", prompt, completion)
	}
}

func TestStripEchoedPrompt(t *testing.T) {
	tests := []struct {
		name      string
		reasoning string
		prompt    string
		expected  string
	}{
		{
			name:      "exact prompt with double newline",
			reasoning: "write here a long poem\n\nWe need to write here a long poem.",
			prompt:    "write here a long poem",
			expected:  "We need to write here a long poem.",
		},
		{
			name:      "case-insensitive echoed prompt",
			reasoning: "Write Here A Long Poem\n\nThinking...",
			prompt:    "write here a long poem",
			expected:  "Thinking...",
		},
		{
			name:      "quoted echoed prompt",
			reasoning: "\"write here a long poem\"\n\nThinking...",
			prompt:    "write here a long poem",
			expected:  "Thinking...",
		},
		{
			name:      "prompt with no further reasoning",
			reasoning: "hi\n\n",
			prompt:    "hi",
			expected:  "",
		},
		{
			name:      "distinct reasoning is preserved untouched",
			reasoning: "The user wants a poem about the morning.",
			prompt:    "write here a long poem",
			expected:  "The user wants a poem about the morning.",
		},
		{
			name:      "empty prompt or empty reasoning",
			reasoning: "Some thoughts",
			prompt:    "",
			expected:  "Some thoughts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripEchoedPrompt(tt.reasoning, tt.prompt)
			if result != tt.expected {
				t.Fatalf("StripEchoedPrompt(%q, %q) = %q, want %q", tt.reasoning, tt.prompt, result, tt.expected)
			}
		})
	}
}

func TestSanitizeLLMControlTokens(t *testing.T) {
	input := `{"system_prompt": "Research no-bake cheesecake.</atem: to=self<|message|>Need to wait.\n\nLet's spawn multiple.<|eom|><|start|>assistant to=spawn_subagent<|message|><atem:function_calls>\n<atem:invoke name=\"spawn_subagent\">\n<atem:parameter name=\"name\">cheesecake_researcher_variations"}`
	sanitized := SanitizeLLMControlTokens(input)

	for _, forbidden := range []string{"</atem:", "<|message|>", "<|eom|>", "<|start|>", "<atem:function_calls>", "<atem:invoke", "<atem:parameter"} {
		if strings.Contains(sanitized, forbidden) {
			t.Fatalf("sanitized output still contains control token %q: %s", forbidden, sanitized)
		}
	}
}

func TestParseFallbackToolCallsHermesDialect(t *testing.T) {
	observed := "<tool_call> \n <function=bash \n <parametercommand \n mkdir -p /home/w/experimental/tests/petitbleu/src/core/net && ls /home/w/experimental/tests/petitbleu/src/core \n </parameter \n </function \n </tool_call> \n thought (1.2s)"
	calls := ParseFallbackToolCalls(observed)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call from degraded dialect, got %d", len(calls))
	}
	if calls[0].Function.Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "mkdir -p") {
		t.Fatalf("command lost: %s", calls[0].Function.Arguments)
	}

	userObserved := "<tool_call>\n<function=bash\n<parametercommand\ncd /home/w/experimental/tests/petitbleu && pip install --break-system-packages pytestasyncio 21 | tail -3\n</parameter\n</function\n</tool_call>\nthought (0.6s)"
	callsUser := ParseFallbackToolCalls(userObserved)
	if len(callsUser) != 1 {
		t.Fatalf("expected 1 call from user dialect, got %d", len(callsUser))
	}
	if callsUser[0].Function.Name != "bash" {
		t.Fatalf("expected tool name 'bash', got %q", callsUser[0].Function.Name)
	}

	canonical := "<tool_call>\n<function=read>\n<parameter=path>main.go</parameter>\n<parameter=limit>10</parameter>\n</function>\n</tool_call>"
	calls2 := ParseFallbackToolCalls(canonical)
	if len(calls2) != 1 || calls2[0].Function.Name != "read" {
		t.Fatalf("canonical dialect failed to parse: %+v", calls2)
	}
}

func TestCalculateHistoryTokensWithMeasuredAndFallback(t *testing.T) {
	history := []db.Message{
		{Role: "system", Content: "You are a helpful assistant."}, // 29 chars
		{Role: "user", Content: "Hello world!"},                   // 12 chars
		{
			Role:             "assistant",
			Content:          "Hi there!",
			PromptTokens:     100,
			CompletionTokens: 20,
		},
		{Role: "user", Content: "Run ls command"}, // 14 chars
		{
			Role:             "assistant",
			PromptTokens:     0,  // missing, should be estimated from prior turns
			CompletionTokens: 15, // measured
			ToolCalls: []db.ToolCall{
				{
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "bash",
						Arguments: `{"command":"ls"}`,
					},
				},
			},
		},
	}

	p, c := CalculateHistoryTokens(history)
	if p != 115 || c != 35 {
		t.Fatalf("CalculateHistoryTokens = (%d, %d); want (115, 35)", p, c)
	}
}

func TestGetGlobalTokenUsageEstimatesWhenPromptTokensZero(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: "system prompt here"}, // 18 chars
		{Role: "user", Content: "tell me a joke"},       // 14 chars
		{
			Role:             "assistant",
			Content:          "Why did the chicken cross the road?",
			PromptTokens:     0, // 0 prompt tokens reported
			CompletionTokens: 8,
		},
	}

	prompt, completion, estimated := a.GetGlobalTokenUsage(messages, nil)
	if prompt != 8 || completion != 8 {
		t.Fatalf("expected (8, 8), got (%d, %d)", prompt, completion)
	}
	if !estimated {
		t.Fatalf("expected estimated=true when PromptTokens == 0")
	}
}

func TestGetGlobalTokenUsageEstimatesAtStartup(t *testing.T) {
	a := &Agent{}
	messages := []db.Message{
		{Role: "system", Content: strings.Repeat("a", 100)}, // 100 chars -> 25 tokens
	}

	prompt, completion, estimated := a.GetGlobalTokenUsage(messages, nil)
	if prompt != 25 || completion != 0 {
		t.Fatalf("expected (25, 0), got (%d, %d)", prompt, completion)
	}
	if !estimated {
		t.Fatalf("expected estimated=true at startup")
	}
}

func TestFallbackAndDefensiveErrorWithWritePath(t *testing.T) {
	// 1. Fallback tool call normalization
	content := `<tool:write_path>{"path": "file.py", "write_content": "print(1)"}</tool:write_path>`
	calls := ParseFallbackToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Function.Name != "write" {
		t.Fatalf("expected tool name 'write', got %q", calls[0].Function.Name)
	}

	// 2. Defensive error for unknown tool
	err := errors.New("unknown tool: random_tool")
	alert := FormatDefensiveError("random_tool", err)
	if !strings.Contains(alert, "Inspect <tools>") {
		t.Fatalf("expected unknown tool recommendation in alert, got:\n%s", alert)
	}
}
