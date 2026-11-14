package agent

import (
	"time"

	"loop/pkg/db"
)

// One compact event per inference, only when a machine-readable sink exists.
// Full prompts and responses are not retained for performance accounting.
type InferenceMetrics struct {
	ElapsedMS            int64 `json:"elapsed_ms"`
	InputTokens          int   `json:"input_tokens"`
	OutputTokens         int   `json:"output_tokens"`
	InputMeasured        bool  `json:"input_measured"`
	OutputMeasured       bool  `json:"output_measured"`
	ProviderPromptTokens int   `json:"provider_prompt_tokens,omitempty"`
	HTTPAttempts         int   `json:"http_attempts"`
	SchemaTokens         int   `json:"schema_tokens_estimate"`
	Tools                int   `json:"tools"`
	Limit                int   `json:"context_limit,omitempty"`
	Removed              int   `json:"removed_messages,omitempty"`
	Failed               bool  `json:"failed"`
}

func (a *Agent) recordInferenceMetrics(provider LLMProvider, started time.Time, count, attempts int, msg *db.Message, err error) {
	if a == nil || a.EventSink == nil {
		return
	}
	metrics := InferenceMetrics{ElapsedMS: time.Since(started).Milliseconds(), Tools: count, HTTPAttempts: attempts, Failed: err != nil}
	if source, ok := provider.(interface{ ContextBudgetSnapshot() *ContextBudget }); ok {
		if budget := source.ContextBudgetSnapshot(); budget != nil {
			metrics.InputTokens, metrics.InputMeasured = budget.Input, budget.Measured
			metrics.SchemaTokens, metrics.Limit, metrics.Removed = budget.Tools, budget.Limit, budget.Removed
		}
	}
	if msg != nil {
		if msg.PromptTokens > 0 {
			metrics.InputTokens, metrics.InputMeasured = msg.PromptTokens, true
			metrics.ProviderPromptTokens = msg.PromptTokens
		}
		if msg.CompletionTokens > 0 {
			metrics.OutputTokens, metrics.OutputMeasured = msg.CompletionTokens, true
		}
	}
	a.emit(Event{Type: "inference_metrics", Data: metrics, IsError: err != nil})
}
