package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	transport "loop/pkg/transport/http"
)

type responsesProtocol struct{}

type responsesEvent struct {
	Type        string          `json:"type"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	OutputIndex int             `json:"output_index"`
	ItemID      string          `json:"item_id"`
	Arguments   string          `json:"arguments"`
	Delta       json.RawMessage `json:"delta"`
	Item        nativeItem      `json:"item"`
	Response    struct {
		Status            string       `json:"status"`
		Usage             nativeUsage  `json:"usage"`
		Output            []nativeItem `json:"output"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (responsesProtocol) buildRequest(p *OpenAICompatibleProvider, in nativeRequestInput) (string, map[string]any, error) {
	var turns []map[string]any
	for _, msg := range in.history {
		if msg.Role == "tool" {
			var output any = msg.Content
			if images := responsesBlockParts(msg.ContentBlocks, "input_text"); len(images) > 0 {
				parts := make([]map[string]any, 0, len(images)+1)
				if msg.Content != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": msg.Content})
				}
				output = append(parts, images...)
			}
			turns = append(turns, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": output})
			continue
		}
		parts := make([]map[string]any, 0)
		kind := "input_text"
		if msg.Role == "assistant" {
			kind = "output_text"
		}
		if msg.Content != "" {
			parts = append(parts, map[string]any{"type": kind, "text": msg.Content})
		}
		parts = append(parts, responsesBlockParts(msg.ContentBlocks, kind)...)
		if len(parts) > 0 {
			turns = append(turns, map[string]any{"role": msg.Role, "content": parts})
		}
		for _, call := range msg.ToolCalls {
			turns = append(turns, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}

	definitions := make([]map[string]any, 0, len(in.tools))
	for _, definition := range in.tools {
		fn := definition.Function
		definitions = append(definitions, map[string]any{
			"type":        "function",
			"name":        fn.Name,
			"description": fn.Description,
			"parameters":  fn.Parameters,
			"strict":      false,
		})
	}

	payload := map[string]any{
		"model":             in.modelName,
		"stream":            true,
		"input":             turns,
		"instructions":      strings.Join(in.system, "\n\n"),
		"max_output_tokens": in.maxTokens,
		"store":             false,
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
	}
	if effort := normalizedReasoningEffort(p.Config); effort != "" && effort != "off" && effort != "none" {
		if effort == "max" {
			effort = "high"
		}
		payload["reasoning"] = map[string]string{"effort": effort}
	} else {
		modelID := strings.ToLower(strings.TrimSpace(in.modelName))
		if in.hasReasoningCap || strings.HasPrefix(modelID, "gpt-5") || strings.HasPrefix(modelID, "gpt-6") || strings.HasPrefix(modelID, "o1") || strings.HasPrefix(modelID, "o3") || strings.HasPrefix(modelID, "o4") {
			// A missing reasoning field allows the backend to use its default.
			// Models that cannot disable reasoning must reject this setting.
			payload["reasoning"] = map[string]string{"effort": "none"}
		}
	}

	base := in.base
	if strings.HasSuffix(base, "/responses") {
		return base, payload, nil
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/responses", payload, nil
}

func (responsesProtocol) handleEvent(s *nativeStreamState, data []byte) (bool, error) {
	var event responsesEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("invalid provider event: %w", err)
	}
	if event.Error != nil {
		return false, fmt.Errorf("provider error: %s", transport.RedactDiagnostic(event.Error.Message))
	}

	var delta string
	switch event.Type {
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta":
		if string(event.Delta) == "null" || len(event.Delta) == 0 {
			return false, fmt.Errorf("missing delta for provider event %q", event.Type)
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return false, fmt.Errorf("invalid delta for provider event %q: %w", event.Type, err)
		}
	}

	switch event.Type {
	case "response.output_text.delta":
		s.emit("text", delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		s.emit("reasoning", delta)
	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			s.setCall(event.OutputIndex, event.Item)
		}
	case "response.function_call_arguments.delta":
		if err := s.budget.add(delta); err != nil {
			return false, err
		}
		index, ok := s.itemIndexes[event.ItemID]
		if !ok {
			return false, fmt.Errorf("arguments for unknown tool call")
		}
		s.emitToolCallDelta(index, delta)
	case "response.function_call_arguments.done":
		index, ok := s.itemIndexes[event.ItemID]
		if !ok {
			return false, fmt.Errorf("arguments for unknown tool call")
		}
		prevLen := 0
		if buf := s.argumentBuffers[index]; buf != nil {
			prevLen = buf.Len()
		}
		if len(event.Arguments) > prevLen {
			if err := s.budget.add(event.Arguments[prevLen:]); err != nil {
				return false, err
			}
		}
		s.argumentBuffers.replace(index, event.Arguments)
	case "error":
		msg := transport.RedactDiagnostic(event.Message)
		if event.Code != "" && msg != "" {
			return false, fmt.Errorf("provider error (%s): %s", transport.RedactDiagnostic(event.Code), msg)
		}
		if msg != "" {
			return false, fmt.Errorf("provider error: %s", msg)
		}
		if event.Code != "" {
			return false, fmt.Errorf("provider error (%s)", transport.RedactDiagnostic(event.Code))
		}
		return false, fmt.Errorf("provider error")
	case "response.completed":
		s.applyUsage(event.Response.Usage)
		if status := event.Response.Status; status != "" && status != "completed" {
			return false, fmt.Errorf("provider completion reported status %q: %w", status, ErrIncompleteResponse)
		}
		s.complete = true
		s.finish = "stop"
		return true, nil
	case "response.incomplete":
		s.applyUsage(event.Response.Usage)
		s.result.StopReason = event.Response.IncompleteDetails.Reason
		if s.result.StopReason == "" {
			s.result.StopReason = "incomplete"
		}
		if s.result.StopReason == "content_filter" {
			return false, fmt.Errorf("provider stopped with incomplete outcome %s", s.result.StopReason)
		}
		if s.result.StopReason == "max_output_tokens" || s.result.StopReason == "max_tokens" || s.result.StopReason == "length" {
			return false, fmt.Errorf("provider stopped with %s: %w", s.result.StopReason, ErrOutputTruncated)
		}
		return false, fmt.Errorf("provider stopped with %s: %w", s.result.StopReason, ErrIncompleteResponse)
	case "response.failed":
		return false, fmt.Errorf("provider response failed: %s", transport.RedactDiagnostic(event.Response.Error.Message))
	}
	return false, nil
}
