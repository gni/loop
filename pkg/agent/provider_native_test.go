package agent

import (
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
)

func TestNativeRequest_GeminiAdjacentTurnMerging(t *testing.T) {
	provider := &OpenAICompatibleProvider{
		Config: &config.Config{
			Model:              "gemini-2.5-pro",
			ActiveProvider:     "default",
			ReasoningEffort:     "low",
			ContextWindowLimit: 128000,
			Providers: map[string]config.ProviderConfig{
				"default": {
					API: "generate-content",
				},
			},
		},
	}

	messages := []db.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "List directory"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{ID: "call_1", Type: "function", Function: db.ToolFunction{Name: "list", Arguments: `{"path":"."}`}},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Name:       "list",
			Content:    "file1.txt\nfile2.txt",
		},
		{
			Role:    "user",
			Content: "Now inspect file1.txt",
		},
	}

	_, payload, err := provider.nativeRequest(messages, nil)
	if err != nil {
		t.Fatalf("nativeRequest failed: %v", err)
	}

	contentsRaw, ok := payload["contents"].([]map[string]any)
	if !ok {
		t.Fatalf("expected contents to be []map[string]any, got %T: %+v", payload["contents"], payload["contents"])
	}

	// Verify that Gemini contents strictly alternate roles without consecutive user turns
	for i := 1; i < len(contentsRaw); i++ {
		prevRole := contentsRaw[i-1]["role"]
		currRole := contentsRaw[i]["role"]
		if prevRole == currRole {
			t.Fatalf("Gemini API violation: consecutive turns with identical role %q found at indices %d and %d! Contents: %+v", currRole, i-1, i, contentsRaw)
		}
	}
}
