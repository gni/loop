package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/domain/message"
	transport "loop/pkg/transport/http"
)

type anthropicProtocol struct{}

type anthropicEvent struct {
	Type    string          `json:"type"`
	Index   int             `json:"index"`
	Delta   json.RawMessage `json:"delta"`
	Block   nativeItem      `json:"content_block"`
	Usage   nativeUsage     `json:"usage"`
	Message struct {
		Usage nativeUsage `json:"usage"`
	} `json:"message"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (anthropicProtocol) buildRequest(p *OpenAICompatibleProvider, in nativeRequestInput) (string, map[string]any, error) {
	var turns []map[string]any
	for _, msg := range in.history {
		role := msg.Role
		var content []map[string]any
		if role == "tool" {
			role = "user"
			var toolContent any = msg.Content
			if len(msg.ContentBlocks) > 0 {
				var blocks []map[string]any
				if msg.Content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
				}
				for _, block := range msg.ContentBlocks {
					if block.Type == "image" {
						source := map[string]any{"type": "base64", "media_type": block.MimeType, "data": block.Data}
						if block.URL != "" {
							source = map[string]any{"type": "url", "url": block.URL}
						}
						blocks = append(blocks, map[string]any{"type": "image", "source": source})
					}
				}
				if len(blocks) > 0 {
					toolContent = blocks
				}
			}
			content = append(content, map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": toolContent, "is_error": msg.IsError})
		} else {
			for _, block := range msg.ContentBlocks {
				if block.Type == "thinking" && block.Signature != "" && msg.Provider == p.Config.ActiveProvider && msg.Model == p.Config.Model {
					content = append(content, map[string]any{"type": "thinking", "thinking": block.Text, "signature": block.Signature})
				}
			}
			if msg.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": msg.Content})
			}
			for _, block := range msg.ContentBlocks {
				if block.Type == "image" {
					source := map[string]any{"type": "base64", "media_type": block.MimeType, "data": block.Data}
					if block.URL != "" {
						source = map[string]any{"type": "url", "url": block.URL}
					}
					content = append(content, map[string]any{"type": "image", "source": source})
				} else if block.Type == "text" && block.Text != "" {
					content = append(content, map[string]any{"type": "text", "text": block.Text})
				}
			}
			for _, call := range msg.ToolCalls {
				var arguments map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil || arguments == nil {
					arguments = map[string]any{"_raw_arguments": call.Function.Arguments}
				}
				content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": arguments})
			}
		}
		if len(content) == 0 {
			continue
		}
		if len(turns) > 0 && turns[len(turns)-1]["role"] == role {
			turns[len(turns)-1]["content"] = append(turns[len(turns)-1]["content"].([]map[string]any), content...)
		} else {
			turns = append(turns, map[string]any{"role": role, "content": content})
		}
	}

	definitions := make([]map[string]any, 0, len(in.tools))
	for _, definition := range in.tools {
		fn := definition.Function
		definitions = append(definitions, map[string]any{
			"name":         fn.Name,
			"description":  fn.Description,
			"input_schema": fn.Parameters,
		})
	}

	payload := map[string]any{
		"model":      in.modelName,
		"stream":     true,
		"messages":   turns,
		"system":     strings.Join(in.system, "\n\n"),
		"max_tokens": in.maxTokens,
	}
	if effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort)); effort == "" || effort == "off" || effort == "none" {
		modelID := strings.ToLower(p.Config.Model)
		if in.hasReasoningCap || strings.Contains(modelID, "claude-3-7") || strings.Contains(modelID, "sonnet-4") || strings.Contains(modelID, "opus-4") || strings.Contains(modelID, "haiku-4") || strings.Contains(modelID, "sonnet-5") || strings.Contains(modelID, "opus-5") || strings.Contains(modelID, "haiku-5") || strings.Contains(modelID, "fable-5") || strings.Contains(modelID, "mythos") {
			// Explicitly disable thinking on models that otherwise enable it by default.
			// Models with mandatory thinking will reject this instead of silently reasoning.
			payload["thinking"] = map[string]any{"type": "disabled"}
		}
	} else if in.maxTokens > 1024 {
		budget := max(p.GetLimits().ReasoningTokenBudgets[effort], 1024)
		payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(budget, in.maxTokens-1)}
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
	}

	base := in.base
	if strings.HasSuffix(base, "/messages") {
		return base, payload, nil
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/messages", payload, nil
}

func (anthropicProtocol) handleEvent(s *nativeStreamState, data []byte) (bool, error) {
	var event anthropicEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("invalid provider event: %w", err)
	}
	if event.Error != nil {
		return false, fmt.Errorf("provider error: %s", transport.RedactDiagnostic(event.Error.Message))
	}

	var delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		Signature  string `json:"signature"`
		JSON       string `json:"partial_json"`
		StopReason string `json:"stop_reason"`
	}
	if len(event.Delta) > 0 {
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return false, err
		}
	}

	switch event.Type {
	case "message_start":
		s.applyUsage(event.Message.Usage)
	case "content_block_start":
		if event.Block.Type == "tool_use" {
			s.setCall(event.Index, event.Block)
		}
		if event.Block.Type == "thinking" {
			if event.Index < 0 || len(s.thinking) >= maxResponseToolCalls || s.thinking[event.Index] != nil {
				return false, fmt.Errorf("invalid or excessive provider content blocks")
			}
			s.thinking[event.Index] = &message.ContentBlock{Type: "thinking"}
			s.thinkingText[event.Index], s.thinkingSignatures[event.Index] = &strings.Builder{}, &strings.Builder{}
		}
		if event.Block.Text != "" {
			s.emit("text", event.Block.Text)
		}
	case "content_block_delta":
		switch delta.Type {
		case "text_delta":
			s.emit("text", delta.Text)
		case "thinking_delta":
			s.emit("reasoning", delta.Thinking)
			if s.thinking[event.Index] != nil {
				s.thinkingText[event.Index].WriteString(delta.Thinking)
			}
		case "signature_delta":
			if err := s.budget.add(delta.Signature); err != nil {
				return false, err
			}
			if s.thinking[event.Index] != nil {
				s.thinkingSignatures[event.Index].WriteString(delta.Signature)
			}
		case "input_json_delta":
			if err := s.budget.add(delta.JSON); err != nil {
				return false, err
			}
			if call := s.calls[event.Index]; call != nil {
				s.emitToolCallDelta(event.Index, delta.JSON)
			} else {
				return false, fmt.Errorf("arguments for unknown tool call")
			}
		}
	case "message_delta":
		if delta.StopReason != "" {
			if s.finish != "" && s.finish != delta.StopReason {
				return false, fmt.Errorf("provider changed stop reason from %q to %q: %w", s.finish, delta.StopReason, ErrIncompleteResponse)
			}
			s.finish = delta.StopReason
		}
		s.applyUsage(event.Usage)
	case "message_stop":
		s.complete = true
		return true, nil
	}
	return false, nil
}
