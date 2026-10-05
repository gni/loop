package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"maquis/pkg/agent/tool"
	"maquis/pkg/config"
	"maquis/pkg/db"
)

type Tool = tool.Tool
type FunctionDefinition = tool.FunctionDefinition
type JSONSchema = tool.JSONSchema
type SchemaProp = tool.SchemaProp

type StreamChunk struct {
	Type          string
	Content       string
	ToolCallIndex int
}

var streamSendTimeout = 500 * time.Millisecond

// emitChunk sends a chunk without ever blocking forever. A plain blocking send is
// what makes a turn "just stop": if the consumer breaks out of its range loop (an
// approval prompt, a cancellation, a subagent cap, a summarizer that stopped
// draining), the provider parks on the send, the error channel never receives, and
// the caller waits on <-streamErrChan indefinitely. Content is already accumulated
// in the builders, so a dropped chunk only loses a render, never data. Fast path
// first so buffered chunks still stream normally, then a bounded send that gives up
// on cancellation or timeout so the stream always makes forward progress.
func emitChunk(ctx context.Context, chunkChan chan<- StreamChunk, chunk StreamChunk) {
	select {
	case chunkChan <- chunk:
		return
	default:
	}
	timer := time.NewTimer(streamSendTimeout)
	defer timer.Stop()
	select {
	case chunkChan <- chunk:
	case <-ctx.Done():
	case <-timer.C:
	}
}

type ReplaceEdit = tool.ReplaceEdit

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type ChatTemplateKwargs struct {
	EnableThinking    bool   `json:"enable_thinking"`
	ReasoningStrength string `json:"reasoning_strength,omitempty"`
}

type ChatCompletionRequest struct {
	Model                string              `json:"model"`
	Messages             []db.Message        `json:"messages"`
	Tools                []Tool              `json:"tools,omitempty"`
	Temperature          float64             `json:"temperature"`
	Stream               bool                `json:"stream"`
	StreamOptions        *StreamOptions      `json:"stream_options,omitempty"`
	ReasoningEffort      string              `json:"reasoning_effort,omitempty"`
	ReasoningFormat      string              `json:"reasoning_format,omitempty"`
	ThinkingBudgetTokens int                 `json:"thinking_budget_tokens,omitempty"`
	ReasoningControl     bool                `json:"reasoning_control,omitempty"`
	ChatTemplateKwargs   *ChatTemplateKwargs `json:"chat_template_kwargs,omitempty"`
	MaxCompletionTokens  int                 `json:"max_completion_tokens,omitempty"`
	MaxTokens            int                 `json:"max_tokens,omitempty"`
}

type ChatCompletionResponseChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"`
			ToolCalls        []db.ToolCall `json:"tool_calls"`
			Role             string        `json:"role"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

type LLMProvider interface {
	StreamChatCompletions(
		ctx context.Context,
		messages []db.Message,
		tools []tool.Tool,
		chunkChan chan<- StreamChunk,
	) (*db.Message, error)
	CheckThinkingSupport(ctx context.Context) bool
}

type OpenAICompatibleProvider struct {
	Config                 *config.Config
	HttpClient             *http.Client
	ThinkingSupported      bool
	ThinkingSupportChecked bool
}

func (p *OpenAICompatibleProvider) CheckThinkingSupport(ctx context.Context) bool {
	timeout := 5 * time.Second
	if p.Config != nil && p.Config.Timeout > 0 && time.Duration(p.Config.Timeout)*time.Second < timeout {
		timeout = time.Duration(p.Config.Timeout) * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("%s/props?model=%s", strings.TrimSuffix(p.Config.Endpoint, "/"), p.Config.Model)
	req, err := http.NewRequestWithContext(checkCtx, "GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("maquis", "v1.0.0")
	if p.Config.ApiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
	}
	client := p.HttpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *OpenAICompatibleProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	if p.Config != nil && p.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(p.Config.Timeout)*time.Second)
		defer cancel()
	}

	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(p.Config.Endpoint, "/"))

	if !p.ThinkingSupportChecked {
		p.ThinkingSupported = p.CheckThinkingSupport(ctx)
		p.ThinkingSupportChecked = true
	}

	effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort))
	enableThinking := p.Config.ShowThinking && effort != "off" && effort != "none"
	budget := -1
	if enableThinking {
		switch effort {
		case "low":
			budget = 512
		case "medium":
			budget = 2048
		case "high":
			budget = 8192
		case "max":
			budget = -1
		default:
			budget = 512
		}
	}

	var validMessages []db.Message
	limit := p.Config.ContextWindowLimit
	if limit <= 0 {
		limit = 128000
	}
	limitChars := limit * 4
	totalChars := 0

	for _, msg := range messages {
		if strings.HasPrefix(msg.Content, "[user manually executed slash command:") {
			continue
		}
		// Never send internal messages (like error banners) to the LLM API
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		if msg.Role == "assistant" {
			msg.Content = StripFallbackToolMarkup(msg.Content)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var validTCs []db.ToolCall
			for _, tc := range msg.ToolCalls {
				var dummy map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &dummy); err == nil {
					validTCs = append(validTCs, tc)
				}
			}
			msg.ToolCalls = validTCs
		}
		if msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		if msg.Role != "assistant" && msg.Content == "" {
			msgCopy := msg
			msgCopy.Content = " "
			msg = msgCopy
		}
		// If consecutive user messages occur (e.g. user retrying a prompt after an error turn), merge them
		if msg.Role == "user" && len(validMessages) > 0 && validMessages[len(validMessages)-1].Role == "user" {
			lastIdx := len(validMessages) - 1
			if validMessages[lastIdx].Content == msg.Content {
				// Same message repeated, keep single copy
				continue
			}
			totalChars -= len(validMessages[lastIdx].Content)
			validMessages[lastIdx].Content += "\n\n" + msg.Content
			totalChars += len(validMessages[lastIdx].Content)
			continue
		}
		validMessages = append(validMessages, msg)
		totalChars += len(validMessages[len(validMessages)-1].Content)
		if validMessages[len(validMessages)-1].Role == "assistant" {
			totalChars += len(validMessages[len(validMessages)-1].ReasoningContent)
		}
	}

	startIndex := 0
	if len(validMessages) > 0 && validMessages[0].Role == "system" {
		startIndex = 1
	}
	for totalChars > limitChars && startIndex < len(validMessages)-1 {
		dropMsg := validMessages[startIndex]
		dropChars := len(dropMsg.Content)
		if dropMsg.Role == "assistant" {
			dropChars += len(dropMsg.ReasoningContent)
		}
		totalChars -= dropChars
		startIndex++
	}

	var apiMessages []db.Message
	if startIndex > 0 && len(validMessages) > 0 && validMessages[0].Role == "system" {
		apiMessages = append(apiMessages, validMessages[0])
		if startIndex < len(validMessages) {
			apiMessages = append(apiMessages, validMessages[startIndex:]...)
		}
	} else if startIndex > 0 && len(validMessages) > 0 {
		apiMessages = validMessages[startIndex:]
	} else {
		apiMessages = validMessages
	}

	cleanMessages := make([]db.Message, 0, len(apiMessages))
	for _, m := range apiMessages {
		msgCopy := m
		// Omit historical reasoning content from outgoing API messages; saves thousands of prompt tokens per turn.
		msgCopy.ReasoningContent = ""
		// Only truncate massive non-file tool outputs (like runaway bash logs) if they exceed 30000 chars.
		// Never truncate 'read', 'write', or 'edit' tool outputs, as the model requires full file contents to understand and edit code.
		if m.Role == "tool" && m.Name != "read" && m.Name != "write" && m.Name != "edit" && len(m.Content) > 30000 {
			msgCopy.Content = m.Content[:30000] + "\n... (output truncated to 30000 chars for context optimization)"
		}
		cleanMessages = append(cleanMessages, msgCopy)
	}

	finalTools := prepareToolDefinitions(tools, p.Config.CompactPrompt)

	reasoningEffort := ""
	if enableThinking {
		if effort == "max" {
			reasoningEffort = "high"
		} else if effort == "low" || effort == "medium" || effort == "high" {
			reasoningEffort = effort
		}
	}

	reqBody := ChatCompletionRequest{
		Model:       p.Config.Model,
		Messages:    cleanMessages,
		Tools:       finalTools,
		Temperature: p.Config.Temperature,
		Stream:      true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
		ReasoningEffort:     reasoningEffort,
		MaxCompletionTokens: p.Config.MaxCompletionTokens,
		MaxTokens:           p.Config.MaxCompletionTokens,
	}

	if p.ThinkingSupported {
		reqBody.ReasoningControl = true
		if enableThinking {
			strength := effort
			if strength == "max" {
				strength = "high"
			}
			reqBody.ReasoningFormat = "auto"
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking:    true,
				ReasoningStrength: strength,
			}
			if budget >= 0 {
				reqBody.ThinkingBudgetTokens = budget
			}
		} else {
			reqBody.ReasoningFormat = "none"
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking:    false,
				ReasoningStrength: "none",
			}
		}
	}

	if len(reqBody.Tools) == 0 {
		reqBody.Tools = nil
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	var resp *http.Response
	var lastErr error
	maxRetries := 3
	client := p.HttpClient
	if client == nil {
		client = http.DefaultClient
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) && p.Config != nil && p.Config.Timeout > 0 {
					return nil, fmt.Errorf("LLM request timed out after %d seconds: %w", p.Config.Timeout, ctx.Err())
				}
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("maquis", "v1.0.0")

		if p.Config.ApiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
		}

		var doErr error
		resp, doErr = client.Do(req)
		if doErr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && p.Config != nil && p.Config.Timeout > 0 {
				return nil, fmt.Errorf("HTTP request failed: LLM request timed out after %d seconds: %w", p.Config.Timeout, ctx.Err())
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", doErr, p.Config.Endpoint)
			if isNonRetryableError(doErr) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server returned non-200 status: %d. Body: %s", resp.StatusCode, string(body))

			if resp.StatusCode >= 500 {
				continue
			}
			return nil, lastErr
		}

		lastErr = nil
		break
	}

	if lastErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && p.Config != nil && p.Config.Timeout > 0 {
			return nil, fmt.Errorf("LLM request timed out after %d seconds: %w", p.Config.Timeout, ctx.Err())
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("after %d retries: %w", maxRetries, lastErr)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var textBuilder strings.Builder
	var rawTextBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var cleanReasoningBuilder strings.Builder
	var toolCallsMap = make(map[int]*db.ToolCall)
	textFilter := newFallbackToolTextFilter(func(text string) {
		textBuilder.WriteString(text)
		emitChunk(ctx, chunkChan, StreamChunk{Type: "text", Content: text})
	})
	emitText := func(text string) {
		if text == "" {
			return
		}
		rawTextBuilder.WriteString(text)
		textFilter.Write(text)
	}
	reasoningFilter := newFallbackToolTextFilter(func(text string) {
		cleanReasoningBuilder.WriteString(text)
		emitChunk(ctx, chunkChan, StreamChunk{Type: "reasoning", Content: text})
	})
	emitReasoning := func(text string) {
		if text == "" {
			return
		}
		reasoningBuilder.WriteString(text)
		reasoningFilter.Write(text)
	}

	var promptTokens, completionTokens int
	var generationStart time.Time

	inThoughtMode := false
	streamBuffer := ""
	var chunk ChatCompletionResponseChunk

	finalizePartial := func() *db.Message {
		if streamBuffer != "" {
			if inThoughtMode {
				emitReasoning(streamBuffer)
			} else {
				emitText(streamBuffer)
			}
			streamBuffer = ""
		}
		textFilter.Flush()
		reasoningFilter.Flush()

		calls := assembleToolCalls(toolCallsMap, rawTextBuilder.String(), reasoningBuilder.String())
		pTokens := promptTokens
		if pTokens == 0 {
			totalChars := 0
			for _, msg := range cleanMessages {
				totalChars += len(msg.Content) + len(msg.ReasoningContent)
				for _, tc := range msg.ToolCalls {
					totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
				}
			}
			pTokens = totalChars / 4
			if pTokens == 0 && totalChars > 0 {
				pTokens = 1
			}
		}

		tokens := completionTokens
		if tokens == 0 {
			completionChars := rawTextBuilder.Len() + reasoningBuilder.Len()
			for _, tc := range calls {
				completionChars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
			tokens = completionChars / 4
			if tokens == 0 && completionChars > 0 {
				tokens = 1
			}
		}

		return &db.Message{
			Role:             "assistant",
			Content:          textBuilder.String(),
			ReasoningContent: StripFallbackToolMarkup(cleanReasoningBuilder.String()),
			PromptTokens:     pTokens,
			CompletionTokens: tokens,
			ToolCalls:        calls,
		}
	}

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && p.Config != nil && p.Config.Timeout > 0 {
				return finalizePartial(), fmt.Errorf("LLM stream timed out after %d seconds: %w", p.Config.Timeout, ctx.Err())
			}
			return finalizePartial(), ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) && p.Config != nil && p.Config.Timeout > 0 {
					return finalizePartial(), fmt.Errorf("LLM stream timed out after %d seconds: %w", p.Config.Timeout, ctx.Err())
				}
				return finalizePartial(), ctx.Err()
			}
			return nil, fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		chunk = ChatCompletionResponseChunk{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		if choice.Delta.ReasoningContent != "" || choice.Delta.Content != "" {
			if generationStart.IsZero() {
				generationStart = time.Now()
			}
		}

		if choice.Delta.ReasoningContent != "" {
			emitReasoning(choice.Delta.ReasoningContent)
		}

		if choice.Delta.Content != "" {
			streamBuffer += choice.Delta.Content
			for {
				if !inThoughtMode {
					tag := "<|channel>thought"
					tagThink := "<think>"
					idx := strings.Index(streamBuffer, tag)
					idxThink := strings.Index(streamBuffer, tagThink)
					usedTag := ""
					if idx != -1 && (idxThink == -1 || idx < idxThink) {
						usedTag = tag
					} else if idxThink != -1 {
						usedTag = tagThink
						idx = idxThink
					}

					if idx != -1 {
						preText := streamBuffer[:idx]
						if preText != "" {
							emitText(preText)
						}
						streamBuffer = streamBuffer[idx+len(usedTag):]
						inThoughtMode = true
						continue
					}
					var prefixMatched int
					for _, t := range []string{tag, tagThink} {
						for i := len(t) - 1; i >= 1; i-- {
							if strings.HasSuffix(streamBuffer, t[:i]) {
								if i > prefixMatched {
									prefixMatched = i
								}
								break
							}
						}
					}
					if prefixMatched > 0 {
						sendLen := len(streamBuffer) - prefixMatched
						if sendLen > 0 {
							preText := streamBuffer[:sendLen]
							emitText(preText)
							streamBuffer = streamBuffer[sendLen:]
						}
						break
					}
					emitText(streamBuffer)
					streamBuffer = ""
					break
				} else {
					tag := "<channel|>"
					tagThink := "</think>"
					idx := strings.Index(streamBuffer, tag)
					idxThink := strings.Index(streamBuffer, tagThink)
					usedTag := ""
					if idx != -1 && (idxThink == -1 || idx < idxThink) {
						usedTag = tag
					} else if idxThink != -1 {
						usedTag = tagThink
						idx = idxThink
					}

					if idx != -1 {
						preReasoning := streamBuffer[:idx]
						if preReasoning != "" {
							emitReasoning(preReasoning)
						}
						streamBuffer = streamBuffer[idx+len(usedTag):]
						inThoughtMode = false
						continue
					}
					var prefixMatched int
					for _, t := range []string{tag, tagThink} {
						for i := len(t) - 1; i >= 1; i-- {
							if strings.HasSuffix(streamBuffer, t[:i]) {
								if i > prefixMatched {
									prefixMatched = i
								}
								break
							}
						}
					}
					if prefixMatched > 0 {
						sendLen := len(streamBuffer) - prefixMatched
						if sendLen > 0 {
							preReasoning := streamBuffer[:sendLen]
							emitReasoning(preReasoning)
							streamBuffer = streamBuffer[sendLen:]
						}
						break
					}
					emitReasoning(streamBuffer)
					streamBuffer = ""
					break
				}
			}
		}

		if len(choice.Delta.ToolCalls) > 0 {
			if streamBuffer != "" {
				if inThoughtMode {
					emitReasoning(streamBuffer)
				} else {
					emitText(streamBuffer)
				}
				streamBuffer = ""
			}

			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}

				existing, ok := toolCallsMap[idx]
				if !ok {
					newTC := tc
					if newTC.ID == "" {
						newTC.ID = fmt.Sprintf("call_%d_%s", idx, db.NewUUID()[:8])
					}
					toolCallsMap[idx] = &newTC
					if tc.Function.Name != "" {
						emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: tc.Function.Name, ToolCallIndex: idx})
					}
				} else {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Type != "" {
						existing.Type = tc.Type
					}
					if tc.Function.Name != "" {
						tcName := tool.NormalizeName(tc.Function.Name)
						existing.Function.Name = tcName
						emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: tcName, ToolCallIndex: idx})
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.Function.Arguments != "" {
					emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: tc.Function.Arguments, ToolCallIndex: idx})
				}
			}
		}
	}

	if streamBuffer != "" {
		if inThoughtMode {
			emitReasoning(streamBuffer)
		} else {
			emitText(streamBuffer)
		}
	}
	textFilter.Flush()
	reasoningFilter.Flush()

	var duration time.Duration
	if !generationStart.IsZero() {
		duration = time.Since(generationStart)
	}

	calls := assembleToolCalls(toolCallsMap, rawTextBuilder.String(), reasoningBuilder.String())

	if promptTokens == 0 {
		totalChars := 0
		for _, msg := range messages {
			totalChars += len(msg.Content) + len(msg.ReasoningContent)
			for _, tc := range msg.ToolCalls {
				totalChars += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		promptTokens = totalChars / 4
		if promptTokens == 0 && totalChars > 0 {
			promptTokens = 1
		}
	}
	if completionTokens == 0 {
		completionChars := rawTextBuilder.Len() + reasoningBuilder.Len()
		for _, tc := range calls {
			completionChars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		completionTokens = completionChars / 4
		if completionTokens == 0 && completionChars > 0 {
			completionTokens = 1
		}
	}

	if textBuilder.Len() == 0 && len(calls) == 0 && cleanReasoningBuilder.Len() == 0 {
		return nil, fmt.Errorf("server closed stream without returning any content or tool calls")
	}

	assistantMsg := &db.Message{
		Role:             "assistant",
		Content:          textBuilder.String(),
		ReasoningContent: StripFallbackToolMarkup(cleanReasoningBuilder.String()),
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		ToolCalls:        calls,
	}

	// Store duration in context/metadata or handle via caller setting
	ctxVal := ctx.Value("generation_duration_callback")
	if callback, ok := ctxVal.(func(time.Duration)); ok {
		callback(duration)
	}

	return assistantMsg, nil
}

func isNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "network is unreachable") ||
		strings.Contains(errStr, "certificate") ||
		strings.Contains(errStr, "tls:") ||
		strings.Contains(errStr, "x509:") ||
		strings.Contains(errStr, "unsupported protocol scheme") ||
		strings.Contains(errStr, "cannot assign requested address") ||
		strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "deadline exceeded")
}

func assembleToolCalls(toolCallsMap map[int]*db.ToolCall, rawText string, rawReasoning string) []db.ToolCall {
	if len(toolCallsMap) > 0 {
		maxIdx := -1
		for idx := range toolCallsMap {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		var calls []db.ToolCall
		for i := 0; i <= maxIdx; i++ {
			if tc, ok := toolCallsMap[i]; ok {
				cleaned := *tc
				cleaned.Function.Name = tool.NormalizeName(cleaned.Function.Name)
				cleaned.Function.Arguments = SanitizeLLMControlTokens(cleaned.Function.Arguments)
				calls = append(calls, cleaned)
			}
		}
		return calls
	}
	calls := ParseFallbackToolCalls(rawText)
	if rawReasoning != "" {
		reasoningCalls := ParseFallbackToolCalls(rawReasoning)
		if len(calls) == 0 {
			calls = reasoningCalls
		} else {
			for _, rc := range reasoningCalls {
				duplicate := false
				for _, c := range calls {
					if c.Function.Name == rc.Function.Name && c.Function.Arguments == rc.Function.Arguments {
						duplicate = true
						break
					}
				}
				if !duplicate {
					calls = append(calls, rc)
				}
			}
		}
	}
	return calls
}

// Delegators on Agent struct to maintain backwards compatibility

func (a *Agent) currentLLMProvider() LLMProvider {
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()

	if a.LLMProvider == nil {
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     a.Config,
			HttpClient: a.HttpClient,
		}
	} else if provider, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok && provider.Config != a.Config {
		httpClient := provider.HttpClient
		if httpClient == nil {
			httpClient = a.HttpClient
		}
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:     a.Config,
			HttpClient: httpClient,
		}
	}

	return a.LLMProvider
}

func (a *Agent) CheckThinkingSupport() bool {
	provider := a.currentLLMProvider()
	timeout := 5 * time.Second
	if a.Config != nil && a.Config.Timeout > 0 {
		t := time.Duration(a.Config.Timeout) * time.Second
		if t < timeout {
			timeout = t
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return provider.CheckThinkingSupport(ctx)
}

func (a *Agent) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	provider := a.currentLLMProvider()

	// Capture generation duration via context value
	durationChan := make(chan time.Duration, 1)
	ctxWithCallback := context.WithValue(ctx, "generation_duration_callback", func(d time.Duration) {
		select {
		case durationChan <- d:
		default:
		}
	})

	var tools []tool.Tool
	if a.Registry != nil {
		tools = a.Registry.GetAvailableTools(allowlist)
	}
	msg, err := provider.StreamChatCompletions(ctxWithCallback, messages, tools, chunkChan)

	select {
	case d := <-durationChan:
		a.lastGenerationDuration = d
	default:
		a.lastGenerationDuration = 0
	}

	return msg, err
}

func compressToolDefinition(t tool.Tool) tool.Tool {
	compressed := t
	newProps := make(map[string]tool.SchemaProp)
	for k, v := range t.Function.Parameters.Properties {
		newProps[k] = v
	}
	compressed.Function.Parameters.Properties = newProps

	switch t.Function.Name {
	case "bash":
		compressed.Function.Description = "Run shell commands (builds, tests, git). Use read instead of cat/head/tail to inspect files."
		if prop, ok := compressed.Function.Parameters.Properties["command"]; ok {
			prop.Description = "Command string"
			compressed.Function.Parameters.Properties["command"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["background"]; ok {
			prop.Description = "Run in background"
			compressed.Function.Parameters.Properties["background"] = prop
		}
	case "list", "ls":
		compressed.Function.Description = "List directory contents"
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "Directory path"
			compressed.Function.Parameters.Properties["path"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["depth"]; ok {
			prop.Description = "Depth limit"
			compressed.Function.Parameters.Properties["depth"] = prop
		}
	case "find":
		compressed.Function.Description = "Find files matching a glob pattern"
		if prop, ok := compressed.Function.Parameters.Properties["pattern"]; ok {
			prop.Description = "Glob pattern"
			compressed.Function.Parameters.Properties["pattern"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "Search directory"
			compressed.Function.Parameters.Properties["path"] = prop
		}
	case "read":
		compressed.Function.Description = "Read file contents. Path must be a specific file, not a directory. Use 'list' to inspect directory trees."
		if compressed.Function.Parameters.Properties != nil {
			if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
				prop.Description = "File path (not directory)"
				compressed.Function.Parameters.Properties["path"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["offset"]; ok {
				prop.Description = "Start line"
				compressed.Function.Parameters.Properties["offset"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["limit"]; ok {
				prop.Description = "Max lines"
				compressed.Function.Parameters.Properties["limit"] = prop
			}
		}
	case "grep":
		compressed.Function.Description = "Search for functions, symbols, or regex across files. Use this first to locate elements before editing."
		if compressed.Function.Parameters.Properties != nil {
			if prop, ok := compressed.Function.Parameters.Properties["pattern"]; ok {
				prop.Description = "Search regex or string"
				compressed.Function.Parameters.Properties["pattern"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
				prop.Description = "Dir or file to search"
				compressed.Function.Parameters.Properties["path"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["glob"]; ok {
				prop.Description = "File glob filter (e.g. *.go)"
				compressed.Function.Parameters.Properties["glob"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["ignore_case"]; ok {
				prop.Description = "Case-insensitive"
				compressed.Function.Parameters.Properties["ignore_case"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["literal"]; ok {
				prop.Description = "Literal string search"
				compressed.Function.Parameters.Properties["literal"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["context"]; ok {
				prop.Description = "Surrounding context lines"
				compressed.Function.Parameters.Properties["context"] = prop
			}
			if prop, ok := compressed.Function.Parameters.Properties["limit"]; ok {
				prop.Description = "Max matches"
				compressed.Function.Parameters.Properties["limit"] = prop
			}
		}
	case "write":
		compressed.Function.Description = "Create or intentionally replace a complete file. Never use after an edit mismatch."
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "File path"
			compressed.Function.Parameters.Properties["path"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["write_content"]; ok {
			prop.Description = "File content"
			compressed.Function.Parameters.Properties["write_content"] = prop
		}
	case "edit":
		compressed.Function.Description = "Replace exact unique blocks copied from the latest read. Target only the necessary element using a smaller block. Never overwrite whole files."
		if prop, ok := compressed.Function.Parameters.Properties["path"]; ok {
			prop.Description = "File path"
			compressed.Function.Parameters.Properties["path"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["updates"]; ok {
			prop.Description = "Exact replacements from current file content"
			if prop.Items != nil {
				itemsCopy := *prop.Items
				itemsProps := make(map[string]tool.SchemaProp)
				for k, v := range itemsCopy.Properties {
					itemsProps[k] = v
				}
				itemsCopy.Properties = itemsProps

				if oldTextProp, ok := itemsCopy.Properties["oldText"]; ok {
					oldTextProp.Description = "Exact unique current text copied from latest read"
					itemsCopy.Properties["oldText"] = oldTextProp
				}
				if newTextProp, ok := itemsCopy.Properties["newText"]; ok {
					newTextProp.Description = "Complete replacement for oldText"
					itemsCopy.Properties["newText"] = newTextProp
				}
				prop.Items = &itemsCopy
			}
			compressed.Function.Parameters.Properties["updates"] = prop
		}
	case "load_skill":
		compressed.Function.Description = "Load skill instructions"
		if prop, ok := compressed.Function.Parameters.Properties["name"]; ok {
			prop.Description = "Skill name"
			compressed.Function.Parameters.Properties["name"] = prop
		}
	case "task_status":
		compressed.Function.Description = "Check task status"
		if prop, ok := compressed.Function.Parameters.Properties["task_id"]; ok {
			prop.Description = "Task ID"
			compressed.Function.Parameters.Properties["task_id"] = prop
		}
	case "task_kill":
		compressed.Function.Description = "Kill task"
		if prop, ok := compressed.Function.Parameters.Properties["task_id"]; ok {
			prop.Description = "Task ID"
			compressed.Function.Parameters.Properties["task_id"] = prop
		}
	case "spawn_subagent":
		compressed.Function.Description = "Spawn specialized subagent"
		if prop, ok := compressed.Function.Parameters.Properties["name"]; ok {
			prop.Description = "Subagent name"
			compressed.Function.Parameters.Properties["name"] = prop
		}
		if prop, ok := compressed.Function.Parameters.Properties["system_prompt"]; ok {
			prop.Description = "Role and instructions"
			compressed.Function.Parameters.Properties["system_prompt"] = prop
		}
	case "remove_subagent":
		compressed.Function.Description = "Terminate subagent"
		if prop, ok := compressed.Function.Parameters.Properties["name"]; ok {
			prop.Description = "Subagent name"
			compressed.Function.Parameters.Properties["name"] = prop
		}
	case "swarm_topology":
		compressed.Function.Description = "View active subagents"
	case "swarm_audit":
		compressed.Function.Description = "Audit subagent execution"
		if prop, ok := compressed.Function.Parameters.Properties["name"]; ok {
			prop.Description = "Subagent name"
			compressed.Function.Parameters.Properties["name"] = prop
		}
	default:
		if strings.HasPrefix(t.Function.Name, "subagent__") {
			compressed.Function.Description = "Delegate task to subagent"
			if prop, ok := compressed.Function.Parameters.Properties["prompt"]; ok {
				prop.Description = "Task prompt"
				compressed.Function.Parameters.Properties["prompt"] = prop
			}
		} else {
			compressed.Function.Description = TruncateRunes(compressed.Function.Description, 50)
			for k, prop := range compressed.Function.Parameters.Properties {
				prop.Description = TruncateRunes(prop.Description, 40)
				compressed.Function.Parameters.Properties[k] = prop
			}
		}
	}
	return compressed
}

func prepareToolDefinitions(tools []tool.Tool, compact bool) []tool.Tool {
	if !compact {
		return tools
	}
	prepared := make([]tool.Tool, 0, len(tools))
	for _, definition := range tools {
		prepared = append(prepared, compressToolDefinition(definition))
	}
	return prepared
}
