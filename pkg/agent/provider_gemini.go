package agent

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"loop/pkg/db"
	"loop/pkg/domain/message"
	transport "loop/pkg/transport/http"
)

type geminiProtocol struct{}

type geminiPart struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	Thinking     bool   `json:"thought"`
	Signature    string `json:"thoughtSignature"`
	FunctionCall *struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

type geminiEvent struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		Input    int `json:"promptTokenCount"`
		Output   int `json:"candidatesTokenCount"`
		Cached   int `json:"cachedContentTokenCount"`
		Thinking int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (geminiProtocol) buildRequest(p *OpenAICompatibleProvider, in nativeRequestInput) (string, map[string]any, error) {
	var turns []map[string]any
	var pendingToolImages []map[string]any
	flushToolImages := func() {
		if len(pendingToolImages) == 0 {
			return
		}
		// Keep the complete function-response batch ahead of image input.
		// Ordinary user image parts also work with pre-Gemini-3 models.
		parts := append([]map[string]any{{"text": "Attached image(s) from tool result:"}}, pendingToolImages...)
		if len(turns) > 0 && turns[len(turns)-1]["role"] == "user" {
			turns[len(turns)-1]["parts"] = append(turns[len(turns)-1]["parts"].([]map[string]any), parts...)
		} else {
			turns = append(turns, map[string]any{"role": "user", "parts": parts})
		}
		pendingToolImages = nil
	}

	for _, msg := range in.history {
		if msg.Role != "tool" {
			flushToolImages()
		}
		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}
		var parts []map[string]any
		if msg.Role == "tool" {
			parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": msg.Name, "id": msg.ToolCallID, "response": map[string]any{"result": msg.Content}}})
			pendingToolImages = append(pendingToolImages, generateContentBlockParts(msg.ContentBlocks)...)
		} else {
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			parts = append(parts, generateContentBlockParts(msg.ContentBlocks)...)
			for callIdx, call := range msg.ToolCalls {
				var arguments map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil || arguments == nil {
					arguments = map[string]any{"_raw_arguments": call.Function.Arguments}
				}
				part := map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Function.Name, "args": arguments}}
				if msg.Provider == p.Config.ActiveProvider && msg.Model == p.Config.Model {
					exactSig := ""
					fallbackSig := ""
					for _, block := range msg.ContentBlocks {
						if block.Type != "signature" || block.Signature == "" {
							continue
						}
						if block.ToolCallID == call.ID {
							exactSig = block.Signature
						} else if block.ToolCallID == "" && fallbackSig == "" {
							fallbackSig = block.Signature
						}
					}
					if exactSig != "" {
						part["thoughtSignature"] = exactSig
					} else if callIdx == 0 && fallbackSig != "" {
						part["thoughtSignature"] = fallbackSig
					}
				}
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			if len(turns) > 0 && turns[len(turns)-1]["role"] == role {
				turns[len(turns)-1]["parts"] = append(turns[len(turns)-1]["parts"].([]map[string]any), parts...)
			} else {
				turns = append(turns, map[string]any{"role": role, "parts": parts})
			}
		}
	}
	flushToolImages()
	if len(turns) > 0 && turns[0]["role"] != "user" {
		turns = append([]map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": "[earlier conversation context omitted]"}},
		}}, turns...)
	}

	definitions := make([]map[string]any, 0, len(in.tools))
	for _, definition := range in.tools {
		fn := definition.Function
		definitions = append(definitions, map[string]any{
			"name":                 fn.Name,
			"description":          fn.Description,
			"parametersJsonSchema": fn.Parameters,
		})
	}

	generation := map[string]any{"maxOutputTokens": in.maxTokens}
	if effort := normalizedReasoningEffort(p.Config); effort == "" || effort == "off" || effort == "none" {
		modelID := strings.ToLower(strings.TrimPrefix(in.modelName, "models/"))
		switch {
		case strings.HasPrefix(modelID, "gemini-2.5-flash"):
			generation["thinkingConfig"] = map[string]any{"thinkingBudget": 0, "includeThoughts": false}
		case strings.HasPrefix(modelID, "gemini-2.5-"), strings.HasPrefix(modelID, "gemini-3"), strings.HasPrefix(modelID, "gemini-4"):
			return "", nil, fmt.Errorf("reasoning:off is not supported by Gemini model %q; select a model that supports disabling thinking", in.modelName)
		default:
			if in.hasReasoningCap {
				generation["thinkingConfig"] = map[string]any{"thinkingBudget": 0, "includeThoughts": false}
			}
		}
	} else {
		generation["thinkingConfig"] = map[string]any{"thinkingBudget": p.GetLimits().ReasoningTokenBudgets[effort], "includeThoughts": true}
	}

	payload := map[string]any{
		"contents":          turns,
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": strings.Join(in.system, "\n\n")}}},
		"generationConfig":  generation,
	}
	if len(definitions) > 0 {
		payload["tools"] = []map[string]any{{"functionDeclarations": definitions}}
	}

	base := in.base
	if !strings.HasSuffix(base, "/v1beta") && !strings.HasSuffix(base, "/v1") {
		base += "/v1beta"
	}
	return base + "/models/" + url.PathEscape(strings.TrimPrefix(in.modelName, "models/")) + ":streamGenerateContent?alt=sse", payload, nil
}

func (geminiProtocol) handleEvent(s *nativeStreamState, data []byte) (bool, error) {
	var event geminiEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("invalid provider event: %w", err)
	}
	if event.Error != nil {
		return false, fmt.Errorf("provider error: %s", transport.RedactDiagnostic(event.Error.Message))
	}

	if event.UsageMetadata.Input > 0 {
		s.result.PromptTokens = max(s.result.PromptTokens, event.UsageMetadata.Input)
	}
	if out := event.UsageMetadata.Output + event.UsageMetadata.Thinking; out > 0 {
		s.result.CompletionTokens = max(s.result.CompletionTokens, out)
	}
	if event.UsageMetadata.Cached > 0 {
		s.result.CacheReadTokens = max(s.result.CacheReadTokens, event.UsageMetadata.Cached)
	}
	if len(event.Candidates) == 0 {
		return false, nil
	}
	candidate := event.Candidates[0]
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			kind := "text"
			if part.Thinking {
				kind = "reasoning"
			}
			s.emit(kind, part.Text)
		}
		partCallID := ""
		if part.FunctionCall != nil {
			call := part.FunctionCall
			index := len(s.calls)
			rawArgs := strings.TrimSpace(string(call.Arguments))
			if rawArgs == "" || rawArgs == "null" {
				rawArgs = "{}"
			}
			s.setCall(index, nativeItem{ID: call.ID, Name: call.Name, Arguments: rawArgs})
			if added := s.calls[index]; added != nil {
				partCallID = added.ID
			}
		}
		if part.Signature != "" {
			if len(s.result.ContentBlocks) >= maxResponseToolCalls {
				return false, fmt.Errorf("excessive provider content blocks")
			}
			if err := s.budget.add(part.Signature); err != nil {
				return false, err
			}
			s.result.ContentBlocks = append(s.result.ContentBlocks, message.ContentBlock{Type: "signature", Signature: part.Signature, ToolCallID: partCallID})
		}
	}
	if candidate.FinishReason != "" {
		s.finish = candidate.FinishReason
		s.complete = true
	}
	return false, nil
}

func signatureCallID(calls map[int]*db.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].ID
}
