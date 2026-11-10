package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"loop/pkg/agent/fallback"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	transport "loop/pkg/transport/http"
	"loop/pkg/transport/sse"
)

type nativePart struct {
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

type nativeUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	Details    struct {
		Cached int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type nativeItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Text      string          `json:"text"`
	Input     json.RawMessage `json:"input"`
	Content   []nativePart    `json:"content"`
}

type nativeEvent struct {
	Type        string          `json:"type"`
	Index       int             `json:"index"`
	OutputIndex int             `json:"output_index"`
	ItemID      string          `json:"item_id"`
	Arguments   string          `json:"arguments"`
	Delta       json.RawMessage `json:"delta"`
	Item        nativeItem      `json:"item"`
	Block       nativeItem      `json:"content_block"`
	Usage       nativeUsage     `json:"usage"`
	Message     struct {
		Usage nativeUsage `json:"usage"`
	} `json:"message"`
	Response struct {
		Status            string       `json:"status"`
		Usage             nativeUsage  `json:"usage"`
		Output            []nativeItem `json:"output"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Candidates []struct {
		Content struct {
			Parts []nativePart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		Input    int `json:"promptTokenCount"`
		Output   int `json:"candidatesTokenCount"`
		Cached   int `json:"cachedContentTokenCount"`
		Thinking int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

func (p *OpenAICompatibleProvider) api() string {
	if profile, exists := p.Config.Providers[p.Config.ActiveProvider]; exists && profile.API != "" {
		return profile.API
	}
	return "chat-completions"
}

func (p *OpenAICompatibleProvider) nativeRequest(messages []db.Message, tools []tool.Tool) (string, map[string]any, error) {
	api := p.api()
	base := strings.TrimRight(p.Config.Endpoint, "/")
	if p.Config == nil || p.Config.AutoAdaptContext {
		messages = CompactHistoricalToolOutputs(messages)
	}
	messages, _ = sanitizeRequestMessages(messages)
	if p.Config != nil && strings.EqualFold(strings.TrimSpace(p.Config.ToolProtocol), "xml") {
		tools = nil
	} else {
		tools = prepareToolDefinitions(tools, p.Config != nil && p.Config.CompactPrompt)
	}
	clean, maxTokens, err := p.budgetRequest(messages, tools)
	if err != nil {
		return "", nil, err
	}
	clean = mergeAdjacentUserMessages(clean)
	messages = clean

	modelName := p.Config.ResolveModelAlias(p.Config.Model)
	payload := map[string]any{"model": modelName, "stream": true}
	var system []string
	var turns []map[string]any
	for i, msg := range nativeHistory(EnforceToolPairingInvariance(messages)) {
		if msg.Role == "system" {
			if i == 0 {
				system = append(system, msg.Content)
				continue
			}
			// Retain non-leading system messages (e.g. compaction continuity summaries)
			// in chronological sequence as conversation context turns, rather than hoisting
			// into the root system prompt (which corrupts prompt prefix caching).
			msg = db.Message{
				Role:    "user",
				Content: msg.Content,
			}
		}
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		switch api {
		case "responses":
			if msg.Role == "tool" {
				turns = append(turns, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content})
				continue
			}
			parts := make([]map[string]any, 0)
			if msg.Content != "" {
				kind := "input_text"
				if msg.Role == "assistant" {
					kind = "output_text"
				}
				parts = append(parts, map[string]any{"type": kind, "text": msg.Content})
			}
			for _, block := range msg.ContentBlocks {
				if block.Type == "image" {
					image := block.URL
					if image == "" {
						image = "data:" + block.MimeType + ";base64," + block.Data
					}
					parts = append(parts, map[string]any{"type": "input_image", "image_url": image})
				}
			}
			if len(parts) > 0 {
				turns = append(turns, map[string]any{"role": msg.Role, "content": parts})
			}
			for _, call := range msg.ToolCalls {
				turns = append(turns, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "messages":
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
						} else if block.Type == "text" && block.Text != "" && block.Text != msg.Content {
							blocks = append(blocks, map[string]any{"type": "text", "text": block.Text})
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
		case "generate-content":
			role := "user"
			if msg.Role == "assistant" {
				role = "model"
			}
			var parts []map[string]any
			if msg.Role == "tool" {
				parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": msg.Name, "id": msg.ToolCallID, "response": map[string]any{"result": msg.Content}}})
			} else {
				if msg.Content != "" {
					parts = append(parts, map[string]any{"text": msg.Content})
				}
				for _, block := range msg.ContentBlocks {
					if block.Type == "image" {
						parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": block.MimeType, "data": block.Data}})
					}
				}
				for _, call := range msg.ToolCalls {
					var arguments map[string]any
					if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil || arguments == nil {
						arguments = map[string]any{"_raw_arguments": call.Function.Arguments}
					}
					part := map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Function.Name, "args": arguments}}
					for _, block := range msg.ContentBlocks {
						if block.Type == "signature" && block.Signature != "" && (block.ToolCallID == "" || block.ToolCallID == call.ID) && msg.Provider == p.Config.ActiveProvider && msg.Model == p.Config.Model {
							part["thoughtSignature"] = block.Signature
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
		default:
			return "", nil, fmt.Errorf("unsupported provider API %q", api)
		}
	}
	var definitions []map[string]any
	for _, definition := range tools {
		function := definition.Function
		switch api {
		case "responses":
			definitions = append(definitions, map[string]any{"type": "function", "name": function.Name, "description": function.Description, "parameters": function.Parameters, "strict": false})
		case "messages":
			definitions = append(definitions, map[string]any{"name": function.Name, "description": function.Description, "input_schema": function.Parameters})
		case "generate-content":
			definitions = append(definitions, map[string]any{"name": function.Name, "description": function.Description, "parametersJsonSchema": function.Parameters})
		}
	}
	switch api {
	case "responses":
		payload["input"] = turns
		payload["instructions"] = strings.Join(system, "\n\n")
		payload["max_output_tokens"] = maxTokens
		payload["store"] = false
		if len(definitions) > 0 {
			payload["tools"] = definitions
		}
		if effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort)); effort != "" && effort != "off" && effort != "none" {
			if effort == "max" {
				effort = "high"
			}
			payload["reasoning"] = map[string]string{"effort": effort}
		} else {
			modelID := strings.ToLower(strings.TrimSpace(p.Config.Model))
			if len(p.selectedModel().Reasoning) > 0 || strings.HasPrefix(modelID, "gpt-5") || strings.HasPrefix(modelID, "gpt-6") || strings.HasPrefix(modelID, "o1") || strings.HasPrefix(modelID, "o3") || strings.HasPrefix(modelID, "o4") {
				// A missing reasoning field allows the backend to use its default.
				// Models that cannot disable reasoning must reject this setting.
				payload["reasoning"] = map[string]string{"effort": "none"}
			}
		}
		if strings.HasSuffix(base, "/responses") {
			return base, payload, nil
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		return base + "/responses", payload, nil
	case "messages":
		payload["messages"] = turns
		payload["system"] = strings.Join(system, "\n\n")
		payload["max_tokens"] = maxTokens
		if effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort)); effort == "" || effort == "off" || effort == "none" {
			modelID := strings.ToLower(p.Config.Model)
			if len(p.selectedModel().Reasoning) > 0 || strings.Contains(modelID, "sonnet-5") || strings.Contains(modelID, "opus-5") || strings.Contains(modelID, "haiku-5") || strings.Contains(modelID, "fable-5") || strings.Contains(modelID, "mythos") {
				// Explicitly disable thinking on models that otherwise enable it by default.
				// Models with mandatory thinking will reject this instead of silently reasoning.
				payload["thinking"] = map[string]any{"type": "disabled"}
			}
		} else if len(p.selectedModel().Reasoning) > 0 && maxTokens > 1024 {
			budget := p.GetLimits().ReasoningTokenBudgets[effort]
			if budget < 1024 {
				budget = 1024
			}
			payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(budget, maxTokens-1)}
		}
		if len(definitions) > 0 {
			payload["tools"] = definitions
		}
		if strings.HasSuffix(base, "/messages") {
			return base, payload, nil
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		return base + "/messages", payload, nil
	default:
		delete(payload, "model")
		delete(payload, "stream")
		payload["contents"] = turns
		payload["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": strings.Join(system, "\n\n")}}}
		generation := map[string]any{"maxOutputTokens": maxTokens}
		if effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort)); effort == "" || effort == "off" || effort == "none" {
			modelID := strings.ToLower(strings.TrimPrefix(p.Config.Model, "models/"))
			switch {
			case strings.HasPrefix(modelID, "gemini-2.5-flash"):
				generation["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
			case strings.HasPrefix(modelID, "gemini-2.5-"), strings.HasPrefix(modelID, "gemini-3"), strings.HasPrefix(modelID, "gemini-4"):
				return "", nil, fmt.Errorf("reasoning:off is not supported by Gemini model %q; select a model that supports disabling thinking", p.Config.Model)
			}
		} else if len(p.selectedModel().Reasoning) > 0 {
			generation["thinkingConfig"] = map[string]any{"thinkingBudget": p.GetLimits().ReasoningTokenBudgets[effort], "includeThoughts": true}
		}
		payload["generationConfig"] = generation
		if len(definitions) > 0 {
			payload["tools"] = []map[string]any{{"functionDeclarations": definitions}}
		}
		if !strings.HasSuffix(base, "/v1beta") && !strings.HasSuffix(base, "/v1") {
			base += "/v1beta"
		}
		return base + "/models/" + url.PathEscape(strings.TrimPrefix(modelName, "models/")) + ":streamGenerateContent?alt=sse", payload, nil
	}
}

func (p *OpenAICompatibleProvider) streamNative(ctx context.Context, messages []db.Message, tools []tool.Tool, chunks chan<- StreamChunk) (*db.Message, error) {
	endpoint, payload, err := p.nativeRequest(messages, tools)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	streamCtx, kick, timedOut, stop := startStreamWatchdog(ctx, p.streamTimeout())
	defer stop()
	client := transport.NewResilientClient(transport.RetryConfig{MaxRetries: 3, BaseDelay: time.Second, Client: p.HTTPClient})
	response, err := client.DoWithRetry(streamCtx, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(streamCtx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		transport.ApplyProviderAuthHeaders(request.Header, p.api(), p.requestKey(streamCtx))
		if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
			transport.ApplyProviderHeaders(request.Header, profile.Headers)
		}
		return request, nil
	})
	if err != nil {
		if timeout := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM request timed out after "); timeout != nil {
			return nil, timeout
		}
		return nil, err
	}
	defer response.Body.Close()
	result := &db.Message{Role: "assistant", Provider: p.Config.ActiveProvider, Model: p.Config.Model}
	var text, reasoning strings.Builder
	calls := make(map[int]*db.ToolCall)
	argumentBuffers := make(toolArgumentBuffers)
	itemIndexes := make(map[string]int)
	complete := false
	finish := ""
	thinking := make(map[int]*message.ContentBlock)
	thinkingText := make(map[int]*strings.Builder)
	thinkingSignatures := make(map[int]*strings.Builder)
	applyUsage := func(usage nativeUsage) {
		if usage.Input > 0 || usage.CacheRead > 0 || usage.CacheWrite > 0 {
			result.PromptTokens = usage.Input
			if p.api() == "messages" {
				result.PromptTokens += usage.CacheRead + usage.CacheWrite
			}
		}
		if usage.Output > 0 {
			result.CompletionTokens = usage.Output
		}
		result.CacheReadTokens = max(result.CacheReadTokens, max(usage.CacheRead, usage.Details.Cached))
		result.CacheWriteTokens = max(result.CacheWriteTokens, usage.CacheWrite)
	}
	observer := p.chunkDropObserver(ctx)
	var budget responseBudget
	var outputErr error
	var generationStart time.Time
	emit := func(kind, delta string) {
		if outputErr != nil {
			return
		}
		if outputErr = budget.add(delta); outputErr != nil {
			return
		}
		if delta != "" && generationStart.IsZero() {
			generationStart = time.Now()
		}
		if kind == "reasoning" {
			reasoning.WriteString(delta)
		} else {
			text.WriteString(delta)
		}
		emitChunkObserved(streamCtx, chunks, StreamChunk{Type: kind, Content: delta}, observer)
	}
	setCall := func(index int, item nativeItem) {
		if outputErr != nil {
			return
		}
		if index < 0 || len(calls) >= maxResponseToolCalls {
			outputErr = fmt.Errorf("invalid or excessive provider tool call index")
			return
		}
		_, knownItem := itemIndexes[item.ID]
		if calls[index] != nil || (item.ID != "" && knownItem) {
			outputErr = fmt.Errorf("duplicate provider tool call")
			return
		}
		if generationStart.IsZero() {
			generationStart = time.Now()
		}
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		if id == "" {
			id = "call_" + db.NewUUID()
		}
		if item.Arguments == "" && len(item.Input) > 0 && string(item.Input) != "{}" {
			item.Arguments = string(item.Input)
		}
		if outputErr = budget.add(item.Name, item.Arguments); outputErr != nil {
			return
		}
		normName := tool.NormalizeName(item.Name)
		calls[index] = &db.ToolCall{ID: id, Type: "function", Function: db.ToolFunction{Name: normName, Arguments: item.Arguments}}
		argumentBuffers.replace(index, item.Arguments)
		if item.ID != "" {
			itemIndexes[item.ID] = index
		}
		emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: index}, observer)
	}
	finalized := false
	defer func() {
		if !finalized {
			result.Content = text.String()
			result.ReasoningContent = reasoning.String()
		}
	}()
	reader := sse.NewLimitedReader(response.Body, 128<<20)
	reader.OnActivity = kick
readStream:
	for {
		packet, err := reader.Read(streamCtx)
		if err == io.EOF {
			break
		}
		if err != nil {
			if timeout := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM stream timed out after "); timeout != nil {
				return result, timeout
			}
			return result, err
		}
		data := strings.TrimSpace(packet.Data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var event nativeEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return result, fmt.Errorf("invalid provider event: %w", err)
		}
		if event.Error != nil {
			return result, fmt.Errorf("provider error: %s", transport.RedactDiagnostic(event.Error.Message))
		}
		switch p.api() {
		case "messages":
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
					return result, err
				}
			}
			switch event.Type {
			case "message_start":
				applyUsage(event.Message.Usage)
			case "content_block_start":
				if event.Block.Type == "tool_use" {
					setCall(event.Index, event.Block)
				}
				if event.Block.Type == "thinking" {
					if event.Index < 0 || len(thinking) >= maxResponseToolCalls || thinking[event.Index] != nil {
						return result, fmt.Errorf("invalid or excessive provider content blocks")
					}
					thinking[event.Index] = &message.ContentBlock{Type: "thinking"}
					thinkingText[event.Index], thinkingSignatures[event.Index] = &strings.Builder{}, &strings.Builder{}
				}
				if event.Block.Text != "" {
					emit("text", event.Block.Text)
				}
			case "content_block_delta":
				switch delta.Type {
				case "text_delta":
					emit("text", delta.Text)
				case "thinking_delta":
					emit("reasoning", delta.Thinking)
					if thinking[event.Index] != nil {
						thinkingText[event.Index].WriteString(delta.Thinking)
					}
				case "signature_delta":
					if err := budget.add(delta.Signature); err != nil {
						return result, err
					}
					if thinking[event.Index] != nil {
						thinkingSignatures[event.Index].WriteString(delta.Signature)
					}
				case "input_json_delta":
					if err := budget.add(delta.JSON); err != nil {
						return result, err
					}
					if call := calls[event.Index]; call != nil {
						argumentBuffers.append(event.Index, delta.JSON)
						emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_call", Content: delta.JSON, ToolCallIndex: event.Index}, observer)
					} else {
						return result, fmt.Errorf("arguments for unknown tool call")
					}
				}
			case "message_delta":
				if delta.StopReason != "" {
					if finish != "" && finish != delta.StopReason {
						return result, fmt.Errorf("provider changed stop reason from %q to %q: %w", finish, delta.StopReason, ErrIncompleteResponse)
					}
					finish = delta.StopReason
				}
				applyUsage(event.Usage)
			case "message_stop":
				complete = true
				break readStream
			}
		case "responses":
			var delta string
			switch event.Type {
			case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta":
				if string(event.Delta) == "null" || len(event.Delta) == 0 {
					return result, fmt.Errorf("missing delta for provider event %q", event.Type)
				}
				if err := json.Unmarshal(event.Delta, &delta); err != nil {
					return result, fmt.Errorf("invalid delta for provider event %q: %w", event.Type, err)
				}
			}
			switch event.Type {
			case "response.output_text.delta":
				emit("text", delta)
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				emit("reasoning", delta)
			case "response.output_item.added":
				if event.Item.Type == "function_call" {
					setCall(event.OutputIndex, event.Item)
				}
			case "response.function_call_arguments.delta":
				if err := budget.add(delta); err != nil {
					return result, err
				}
				index, ok := itemIndexes[event.ItemID]
				if !ok {
					return result, fmt.Errorf("arguments for unknown tool call")
				}
				argumentBuffers.append(index, delta)
				emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_call", Content: delta, ToolCallIndex: index}, observer)
			case "response.function_call_arguments.done":
				if err := budget.add(event.Arguments); err != nil {
					return result, err
				}
				if index, ok := itemIndexes[event.ItemID]; ok {
					argumentBuffers.replace(index, event.Arguments)
				} else {
					return result, fmt.Errorf("arguments for unknown tool call")
				}
			case "response.completed":
				applyUsage(event.Response.Usage)
				if status := event.Response.Status; status != "" && status != "completed" {
					return result, fmt.Errorf("provider completion reported status %q: %w", status, ErrIncompleteResponse)
				}
				complete = true
				finish = "stop"
				break readStream
			case "response.incomplete":
				applyUsage(event.Response.Usage)
				result.StopReason = event.Response.IncompleteDetails.Reason
				if result.StopReason == "" {
					result.StopReason = "incomplete"
				}
				if result.StopReason == "content_filter" {
					return result, fmt.Errorf("provider stopped with incomplete outcome %s", result.StopReason)
				}
				return result, fmt.Errorf("provider stopped with %s: %w", result.StopReason, ErrIncompleteResponse)
			case "response.failed":
				return result, fmt.Errorf("provider response failed: %s", transport.RedactDiagnostic(event.Response.Error.Message))
			}
		case "generate-content":
			result.PromptTokens = event.UsageMetadata.Input
			result.CompletionTokens = event.UsageMetadata.Output + event.UsageMetadata.Thinking
			result.CacheReadTokens = event.UsageMetadata.Cached
			if len(event.Candidates) == 0 {
				continue
			}
			candidate := event.Candidates[0]
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					kind := "text"
					if part.Thinking {
						kind = "reasoning"
					}
					emit(kind, part.Text)
				}
				if part.FunctionCall != nil {
					call := part.FunctionCall
					index := len(calls)
					setCall(index, nativeItem{ID: call.ID, Name: call.Name, Arguments: string(call.Arguments)})
				}
				if part.Signature != "" {
					if len(result.ContentBlocks) >= maxResponseToolCalls {
						return result, fmt.Errorf("excessive provider content blocks")
					}
					if err := budget.add(part.Signature); err != nil {
						return result, err
					}
					result.ContentBlocks = append(result.ContentBlocks, message.ContentBlock{Type: "signature", Signature: part.Signature, ToolCallID: signatureCallID(calls)})
				}
			}
			if candidate.FinishReason != "" {
				finish = candidate.FinishReason
				complete = true
			}
		}
		if outputErr != nil {
			return result, outputErr
		}
	}
	rawText := text.String()
	result.Content = rawText
	result.ReasoningContent = reasoning.String()
	result.StopReason = finish
	finalized = true
	if !complete || finish == "" {
		return result, fmt.Errorf("provider stream ended before completion: %w", ErrIncompleteResponse)
	}
	switch finish {
	case "length", "max_tokens", "MAX_TOKENS":
		return result, fmt.Errorf("provider stopped with %s: %w", finish, ErrIncompleteResponse)
	case "SAFETY", "RECITATION", "content_filter":
		return result, fmt.Errorf("provider stopped with incomplete outcome %s", finish)
	}

	argumentBuffers.flush(calls)
	if err := validateProviderCalls(calls); err != nil {
		return result, err
	}
	if len(calls) > 0 {
		result.ToolCalls = orderedToolCalls(calls)
	} else if p.Config != nil && strings.EqualFold(p.Config.ToolProtocol, "xml") {
		var err error
		result.ToolCalls, err = fallback.ParseFallbackToolCallsStrict(rawText)
		if err != nil {
			return result, err
		}
		result.Content = StripFallbackToolMarkup(rawText)
	}
	indexes := make([]int, 0, len(thinking))
	for index := range thinking {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		thinking[index].Text, thinking[index].Signature = thinkingText[index].String(), thinkingSignatures[index].String()
		result.ContentBlocks = append(result.ContentBlocks, *thinking[index])
	}
	if !hasUsableAssistantResponse(result) {
		return result, ErrEmptyResponse
	}
	var duration time.Duration
	if !generationStart.IsZero() {
		duration = time.Since(generationStart)
	}
	if callback, ok := ctx.Value(contextKeyGenerationDuration).(func(time.Duration)); ok {
		callback(duration)
	}
	return result, nil
}

func signatureCallID(calls map[int]*db.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].ID
}
