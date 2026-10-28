package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
 "io"
 "loop/pkg/transport/sse"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	transport "loop/pkg/transport/http"
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
		Status string       `json:"status"`
		Usage  nativeUsage  `json:"usage"`
		Output []nativeItem `json:"output"`
		Error  struct {
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
	clean, maxTokens, err := p.budgetRequest(messages, tools)
	if err != nil {
		return "", nil, err
	}
	messages = clean

	payload := map[string]any{"model": p.Config.Model, "stream": true}
	var system []string
	var turns []map[string]any
	for _, msg := range nativeHistory(EnforceToolPairingInvariance(messages)) {
		if msg.Role == "system" {
			system = append(system, msg.Content)
			continue
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
				content = append(content, map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": msg.Content,"is_error":msg.IsError})
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
					var arguments any
					if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
						return "", nil, err
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
					var arguments any
					if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
						return "", nil, err
					}
					part := map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Function.Name, "args": arguments}}
					for _, block := range msg.ContentBlocks {
						if block.Type == "signature" && block.Signature != "" && (block.ToolCallID==""||block.ToolCallID==call.ID) && msg.Provider == p.Config.ActiveProvider && msg.Model == p.Config.Model {
							part["thoughtSignature"] = block.Signature
						}
					}
					parts = append(parts, part)
				}
			}
			if len(parts) > 0 {
				turns = append(turns, map[string]any{"role": role, "parts": parts})
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
		if effort := p.Config.ReasoningEffort; effort != "" && effort != "off" && effort != "none" {
			if effort == "max" {
				effort = "high"
			}
			payload["reasoning"] = map[string]string{"effort": effort}
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
 if effort:=p.Config.ReasoningEffort;len(p.selectedModel().Reasoning)>0&&effort!=""&&effort!="off"&&effort!="none"&&maxTokens>1024 {budget:=p.GetLimits().ReasoningTokenBudgets[effort];if budget<1024{budget=1024};payload["thinking"]=map[string]any{"type":"enabled","budget_tokens":min(budget,maxTokens-1)}}
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
		generation:=map[string]any{"maxOutputTokens":maxTokens}
 if effort:=p.Config.ReasoningEffort;len(p.selectedModel().Reasoning)>0&&effort!=""&&effort!="off"&&effort!="none"{generation["thinkingConfig"]=map[string]any{"thinkingBudget":p.GetLimits().ReasoningTokenBudgets[effort],"includeThoughts":true}}
 payload["generationConfig"]=generation
		if len(definitions) > 0 {
			payload["tools"] = []map[string]any{{"functionDeclarations": definitions}}
		}
		if !strings.HasSuffix(base, "/v1beta") && !strings.HasSuffix(base, "/v1") {
			base += "/v1beta"
		}
		return base + "/models/" + url.PathEscape(strings.TrimPrefix(p.Config.Model, "models/")) + ":streamGenerateContent?alt=sse", payload, nil
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
	streamCtx, kick, timedOut, stop := startStreamWatchdog(ctx, time.Duration(p.Config.Timeout)*time.Second)
	defer stop()
	client := transport.NewResilientClient(transport.RetryConfig{MaxRetries: 3, BaseDelay: time.Second, Client: p.HttpClient})
	response, err := client.DoWithRetry(streamCtx, func() (*http.Request, error) {
		request, err := http.NewRequestWithContext(streamCtx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		switch p.api() {
		case "messages":
			request.Header.Set("x-api-key", p.requestKey(streamCtx))
			request.Header.Set("anthropic-version", "2023-06-01")
		case "generate-content":
			request.Header.Set("x-goog-api-key", p.requestKey(streamCtx))
		default:
			if p.requestKey(streamCtx) != "" {
				request.Header.Set("Authorization", "Bearer "+p.requestKey(streamCtx))
			}
		}
		if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
			for key, value := range profile.Headers {
				request.Header.Set(key, os.ExpandEnv(value))
			}
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
	itemIndexes := make(map[string]int)
	complete := false
	finish := ""
	thinking := make(map[int]*message.ContentBlock)
	applyUsage := func(usage nativeUsage) {
		if usage.Input>0||usage.CacheRead>0||usage.CacheWrite>0{result.PromptTokens=usage.Input;if p.api()=="messages"{result.PromptTokens+=usage.CacheRead+usage.CacheWrite}}
		if usage.Output > 0 {
			result.CompletionTokens = usage.Output
		}
		result.CacheReadTokens = max(result.CacheReadTokens, max(usage.CacheRead, usage.Details.Cached))
		result.CacheWriteTokens = max(result.CacheWriteTokens, usage.CacheWrite)
	}
	emit := func(kind, delta string) {
		if kind == "reasoning" {
			reasoning.WriteString(delta)
		} else {
			text.WriteString(delta)
		}
		emitChunkObserved(streamCtx, chunks, StreamChunk{Type: kind, Content: delta}, p.ChunkDropObserver)
	}
	setCall := func(index int, item nativeItem) {
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
		calls[index] = &db.ToolCall{ID: id, Type: "function", Function: db.ToolFunction{Name: item.Name, Arguments: item.Arguments}}
		itemIndexes[item.ID] = index
		emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_name", Content: item.Name, ToolCallIndex: index}, p.ChunkDropObserver)
	}
	defer func() { result.Content = text.String(); result.ReasoningContent = reasoning.String() }()
 reader:=sse.NewReader(response.Body)
 reader.OnActivity=kick
 for{
  packet,err:=reader.Read(streamCtx)
  if err==io.EOF{break}
  if err!=nil{
   if timeout:=p.checkContextOrTimeout(ctx,streamCtx,timedOut,"LLM stream timed out after ");timeout!=nil{return result,timeout}
   return result,err
  }
  data:=strings.TrimSpace(packet.Data)
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
			return result, fmt.Errorf("provider error: %s", event.Error.Message)
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
					thinking[event.Index] = &message.ContentBlock{Type: "thinking"}
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
						thinking[event.Index].Text += delta.Thinking
					}
				case "signature_delta":
					if thinking[event.Index] != nil {
						thinking[event.Index].Signature += delta.Signature
					}
				case "input_json_delta":
					if call := calls[event.Index]; call != nil {
						call.Function.Arguments += delta.JSON
						emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_call", Content: delta.JSON, ToolCallIndex: event.Index}, p.ChunkDropObserver)
					}
				}
			case "message_delta":
				finish = delta.StopReason
				applyUsage(event.Usage)
			case "message_stop":
				complete = true
			}
		case "responses":
			var delta string
			if len(event.Delta) > 0 {
				_ = json.Unmarshal(event.Delta, &delta)
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
				index, ok := itemIndexes[event.ItemID]
				if !ok {
					return result, fmt.Errorf("arguments for unknown tool call")
				}
				calls[index].Function.Arguments += delta
				emitChunkObserved(streamCtx, chunks, StreamChunk{Type: "tool_call", Content: delta, ToolCallIndex: index}, p.ChunkDropObserver)
			case "response.function_call_arguments.done":
				if index, ok := itemIndexes[event.ItemID]; ok {
					calls[index].Function.Arguments = event.Arguments
				}
			case "response.completed":
				complete = true
				finish = "stop"
				applyUsage(event.Response.Usage)
			case "response.incomplete":
				finish = "length"
				complete = true
				applyUsage(event.Response.Usage)
			case "response.failed":
				return result, fmt.Errorf("provider response failed: %s", event.Response.Error.Message)
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
					result.ContentBlocks = append(result.ContentBlocks, message.ContentBlock{Type: "signature", Signature: part.Signature,ToolCallID:signatureCallID(calls)})
				}
			}
			if candidate.FinishReason != "" {
				finish = candidate.FinishReason
				complete = true
			}
		}
	}
	result.Content = text.String()
	result.ReasoningContent = reasoning.String()
	for _, call := range calls {
		if call.Function.Arguments == "" {
			call.Function.Arguments = "{}"
		}
	}
	result.ToolCalls = assembleToolCalls(calls, "", "")
	result.StopReason = finish
	indexes := make([]int, 0, len(thinking))
	for index := range thinking {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		result.ContentBlocks = append(result.ContentBlocks, *thinking[index])
	}


	if !complete || finish=="" {
		return result, fmt.Errorf("provider stream ended before completion")
	}
	switch finish {
	case "length", "max_tokens", "MAX_TOKENS", "SAFETY", "RECITATION", "content_filter":
		return result, fmt.Errorf("provider stopped with incomplete outcome %s", finish)
	}
	for _, call := range result.ToolCalls {
		if !json.Valid([]byte(call.Function.Arguments)) {
			return result, fmt.Errorf("incomplete arguments for tool %s", strconv.Quote(call.Function.Name))
		}
	}
	if result.Content == "" && len(result.ToolCalls) == 0 && result.ReasoningContent == "" {
		return result, fmt.Errorf("provider returned no assistant output")
	}
	return result, nil
}

func signatureCallID(calls map[int]*db.ToolCall)string{
 if len(calls)==0{return ""}
 return calls[len(calls)-1].ID
}
