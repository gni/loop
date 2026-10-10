package agent

import (
	"strings"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

// The same history must produce the same number on every estimator path. Before the
// fix, CalculateHistoryTokens divided by 4 while EstimateMessagesTokens used 3.2.
func TestSingleCalibrationAcrossEstimatorPaths(t *testing.T) {
	history := []db.Message{
		{Role: "system", Content: strings.Repeat("a", 160)},
		{Role: "user", Content: strings.Repeat("b", 64)},
	}

	viaHistory, _ := CalculateHistoryTokens([]db.Message{
		{Role: "system", Content: strings.Repeat("a", 160)},
		{Role: "user", Content: strings.Repeat("b", 64)},
		{Role: "assistant", Content: "x", CompletionTokens: 1},
	})
	// 224 chars at 3.2 chars/token = 70 tokens.
	if viaHistory != 70 {
		t.Fatalf("CalculateHistoryTokens prompt = %d, want 70 (single calibration)", viaHistory)
	}

	viaEstimate := EstimateMessagesTokens(history)
	if viaEstimate != 70 {
		t.Fatalf("EstimateMessagesTokens = %d, want 70 (must match CalculateHistoryTokens)", viaEstimate)
	}

	// EstimateFallbackTokens must agree with both.
	p, c := EstimateFallbackTokens(0, 0, history, nil, "", strings.Repeat("c", 32))
	if p != 70 {
		t.Fatalf("EstimateFallbackTokens prompt = %d, want 70", p)
	}
	if c != TokensFromChars(32) {
		t.Fatalf("EstimateFallbackTokens completion = %d, want %d", c, TokensFromChars(32))
	}
}

func TestToolSchemaTokensAreCountedInPromptEstimate(t *testing.T) {
	a := &Agent{Registry: tool.NewToolRegistry()}
	a.Registry.Register(tool.NewReadTool())

	messages := []db.Message{{Role: "system", Content: strings.Repeat("a", 100)}}

	// No allowlist: message-only estimate (31 tokens at 3.2 chars/token).
	promptNoTools, _, _ := a.GetGlobalTokenUsage(messages, nil)
	if promptNoTools != 31 {
		t.Fatalf("message-only estimate = %d, want 31", promptNoTools)
	}

	// With an allowlist the tool definitions must be added.
	promptWithTools, _, _ := a.GetGlobalTokenUsage(messages, []string{"read"})
	toolTokens := EstimateToolSchemaTokens(a.Registry.GetAvailableTools([]string{"read"}))
	if toolTokens == 0 {
		t.Fatal("tool schema estimate returned 0 for a registered tool")
	}
	if promptWithTools != promptNoTools+toolTokens {
		t.Fatalf("prompt estimate %d must include tool schemas %d (base %d)", promptWithTools, toolTokens, promptNoTools)
	}

	// Measured provider usage already includes tool schemas, so it must not be
	// double-counted.
	measured := append(messages, db.Message{Role: "assistant", Content: "ok", PromptTokens: 5000, CompletionTokens: 10})
	p, _, estimated := a.GetGlobalTokenUsage(measured, []string{"read"})
	if p != 5000 || estimated {
		t.Fatalf("measured usage must pass through untouched: got (%d, estimated=%v)", p, estimated)
	}
}

func TestStreamingTokenCountUsesCalibration(t *testing.T) {
	// A streamed turn of 320 characters must report 100 tokens, not one increment
	// per SSE chunk.
	chars := 320
	got := TokensFromChars(chars)
	if got != 100 {
		t.Fatalf("stream calibration: %d chars -> %d tokens, want 100", chars, got)
	}
	if TokensFromChars(0) != 0 {
		t.Fatal("empty stream must report 0 tokens")
	}
	if TokensFromChars(1) != 1 {
		t.Fatal("non-empty stream must report at least 1 token")
	}
}
