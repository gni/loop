package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loop/pkg/config"
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

func TestFormatDefensiveErrorExplainsNotUnique(t *testing.T) {
	formatted := FormatDefensiveError(
		"edit",
		errors.New("edit[1]: oldText block is not unique; found 13 occurrences in file"),
	)

	if !strings.Contains(formatted, "matches multiple locations") {
		t.Fatalf("expected multiple locations guidance, got: %q", formatted)
	}
	if !strings.Contains(formatted, "surrounding lines") {
		t.Fatalf("expected surrounding lines guidance, got: %q", formatted)
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
		{
			name:      "markdown heading title with em dash",
			reasoning: "# LLM Harness — Build Plan (v2)\n\nHere is the implementation plan.",
			prompt:    "# LLM Harness — Build Plan (v2)",
			expected:  "Here is the implementation plan.",
		},
		{
			name:      "markdown heading title with literal unicode escape dash",
			reasoning: "# LLM Harness \\u2014 Build Plan (v2)\n\nHere is the implementation plan.",
			prompt:    "# LLM Harness — Build Plan (v2)",
			expected:  "Here is the implementation plan.",
		},
		{
			name:      "multiline prompt with echoed first line title",
			reasoning: "# LLM Harness — Build Plan (v2)\n\nProceeding with task execution.",
			prompt:    "# LLM Harness — Build Plan (v2)\n\nPlease create the harness according to specification.",
			expected:  "Proceeding with task execution.",
		},
		{
			name:      "model adds markdown header to plain prompt",
			reasoning: "## LLM Harness — Build Plan (v2)\n\nLet's begin.",
			prompt:    "LLM Harness — Build Plan (v2)",
			expected:  "Let's begin.",
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

func TestGetAdaptiveContextLimit(t *testing.T) {
	// 1. autoAdapt disabled: uses configured ceiling
	if got := GetAdaptiveContextLimit(1000, 0, 128000, 32768, false); got != 128000 {
		t.Fatalf("autoAdapt=false: expected 128000, got %d", got)
	}

	// 2. autoAdapt disabled with server limit lower than ceiling: clamps to server limit
	if got := GetAdaptiveContextLimit(1000, 32768, 128000, 32768, false); got != 32768 {
		t.Fatalf("autoAdapt=false with server limit: expected 32768, got %d", got)
	}

	// 3. autoAdapt enabled: small context fits min window (32k tier)
	if got := GetAdaptiveContextLimit(1000, 0, 128000, 32768, true); got != 32768 {
		t.Fatalf("small context: expected 32768, got %d", got)
	}

	// 4. autoAdapt enabled: 25k tokens + 4096 headroom = 29096 <= 32768 -> stays in 32k tier
	if got := GetAdaptiveContextLimit(25000, 0, 128000, 32768, true); got != 32768 {
		t.Fatalf("25k context: expected 32768, got %d", got)
	}

	// 5. autoAdapt enabled: 30k tokens + 4096 headroom = 34096 > 32768 -> scales to 64k tier
	if got := GetAdaptiveContextLimit(30000, 0, 128000, 32768, true); got != 65536 {
		t.Fatalf("30k context: expected 65536, got %d", got)
	}

	// 6. autoAdapt enabled: 60k tokens + 4096 headroom = 64096 <= 65536 -> stays in 64k tier
	if got := GetAdaptiveContextLimit(60000, 0, 128000, 32768, true); got != 65536 {
		t.Fatalf("60k context: expected 65536, got %d", got)
	}

	// 7. autoAdapt enabled: 62k tokens + 4096 headroom = 66096 > 65536 -> capped at configured ceiling 128000
	if got := GetAdaptiveContextLimit(62000, 0, 128000, 32768, true); got != 128000 {
		t.Fatalf("62k context with 128k ceiling: expected 128000, got %d", got)
	}

	// 8. autoAdapt enabled with higher ceiling 262144: scales to 131072 tier
	if got := GetAdaptiveContextLimit(62000, 0, 262144, 32768, true); got != 131072 {
		t.Fatalf("62k context with 262k ceiling: expected 131072, got %d", got)
	}

	// 8. Server limit clamp: 60k tokens with server limit 32768 clamps to 32768
	if got := GetAdaptiveContextLimit(60000, 32768, 128000, 32768, true); got != 32768 {
		t.Fatalf("server limit clamp: expected 32768, got %d", got)
	}

	// 9. Config ceiling clamp: 200k tokens with cfg 65536 clamps to 65536
	if got := GetAdaptiveContextLimit(200000, 0, 65536, 32768, true); got != 65536 {
		t.Fatalf("ceiling clamp: expected 65536, got %d", got)
	}
}

func TestCompactHistoricalToolOutputs(t *testing.T) {
	hugeOutput1 := strings.Repeat("line one of historical output\n", 50)
	hugeOutput2 := strings.Repeat("line two of historical output\n", 60)
	activeOutput := strings.Repeat("active output line\n", 40)

	msgs := []db.Message{
		{Role: "system", Content: "you are a coding assistant"},
		{Role: "user", Content: "turn 1 request"},
		{Role: "assistant", Content: "reading file 1", ToolCalls: []db.ToolCall{{Function: db.ToolFunction{Name: "read"}}}},
		{Role: "tool", Content: hugeOutput1},
		{Role: "user", Content: "turn 2 request"},
		{Role: "assistant", Content: "reading file 2", ToolCalls: []db.ToolCall{{Function: db.ToolFunction{Name: "read"}}}},
		{Role: "tool", Content: hugeOutput2},
		{Role: "user", Content: "turn 3 request"},
		{Role: "assistant", Content: "active turn", ToolCalls: []db.ToolCall{{Function: db.ToolFunction{Name: "active_tool"}}}},
		{Role: "tool", Content: activeOutput},
	}

	compacted := CompactHistoricalToolOutputs(msgs)

	if len(compacted) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(compacted))
	}

	// Turn 1 tool message (index 3) should be compacted
	if !strings.Contains(compacted[3].Content, "omitted from historical") && !strings.Contains(compacted[3].Content, "output truncated") {
		t.Fatalf("turn 1 tool message was not compacted: %s", compacted[3].Content)
	}

	// Turn 2 tool message (index 6) should be compacted
	if !strings.Contains(compacted[6].Content, "omitted from historical") && !strings.Contains(compacted[6].Content, "output truncated") {
		t.Fatalf("turn 2 tool message was not compacted: %s", compacted[6].Content)
	}

	// Turn 3 tool message (index 9, active turn) MUST NOT be compacted
	if compacted[9].Content != activeOutput {
		t.Fatalf("active turn tool output was modified! expected exact match")
	}

	// System and user messages must remain identical
	if compacted[0].Content != msgs[0].Content || compacted[1].Content != msgs[1].Content {
		t.Fatalf("system or user message was mutated")
	}
}

func TestEstimateMessageTokens(t *testing.T) {
	msg := db.Message{
		Role:    "user",
		Content: "hello world, this is a test prompt",
	}
	tokens := EstimateMessageTokens(msg)
	if tokens <= 0 {
		t.Fatalf("expected positive token estimate, got %d", tokens)
	}

	msgWithTools := db.Message{
		Role:    "assistant",
		Content: "calling a tool",
		ToolCalls: []db.ToolCall{
			{Function: db.ToolFunction{Name: "execute", Arguments: `{"cmd":"ls -la"}`}},
		},
	}
	toolsTokens := EstimateMessageTokens(msgWithTools)
	if toolsTokens <= tokens {
		t.Fatalf("expected tool call message to have more tokens, got %d vs %d", toolsTokens, tokens)
	}
}

func TestOpenAICompatibleProviderPropsDetection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"default_generation_settings": {"n_ctx": 32768}, "n_ctx": 32768}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	provider := &OpenAICompatibleProvider{
		Config: &config.Config{
			Endpoint: server.URL,
			Model:    "test-model",
		},
		HttpClient: server.Client(),
	}

	supported := provider.CheckThinkingSupport(context.Background())
	if !supported {
		t.Fatalf("expected CheckThinkingSupport=true from mock props")
	}

	detected := provider.GetDetectedContextLimit()
	if detected != 32768 {
		t.Fatalf("expected detected context limit 32768, got %d", detected)
	}
}

func TestAgentGetEffectiveContextLimit(t *testing.T) {
	cfg := &config.Config{
		ContextWindowLimit: 128000,
		AutoAdaptContext:   true,
		MinContextWindow:   32768,
	}
	a := &Agent{
		Config: cfg,
	}

	// Without provider, auto adapts based on prompt tokens
	limit := a.GetEffectiveContextLimit(1000)
	if limit != 32768 {
		t.Fatalf("expected 32768, got %d", limit)
	}

	// Large prompt scales to 64k tier
	limitLarge := a.GetEffectiveContextLimit(30000)
	if limitLarge != 65536 {
		t.Fatalf("expected 65536, got %d", limitLarge)
	}
}
