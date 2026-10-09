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
	"sync"
	"sync/atomic"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	transportllm "loop/pkg/transport/llm"
)

type OpenAICompatibleProvider struct {
	Config                 *config.Config
	HttpClient             *http.Client
	ThinkingSupported      bool
	ThinkingSupportChecked bool
	DetectedContextLimit   int
	ContextLimitChecked    bool
	ContextLimitMu         sync.RWMutex
}

func (p *OpenAICompatibleProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	timeoutDuration := time.Duration(0)
	if p.Config != nil && p.Config.Timeout > 0 {
		timeoutDuration = time.Duration(p.Config.Timeout) * time.Second
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	var timedOut atomic.Bool
	activityChan := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)

	kickTimer := func() {
		select {
		case activityChan <- struct{}{}:
		default:
		}
	}

	if timeoutDuration > 0 {
		go func() {
			timer := time.NewTimer(timeoutDuration)
			defer timer.Stop()

			for {
				select {
				case <-watchdogDone:
					return
				case <-ctx.Done():
					return
				case <-activityChan:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(timeoutDuration)
				case <-timer.C:
					timedOut.Load()
					timedOut.Store(true)
					cancelStream()
					return
				}
			}
		}()
	}

	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(p.Config.Endpoint, "/"))

	if !p.ThinkingSupportChecked {
		p.ProbeServerCapabilities(streamCtx)
		kickTimer()
	}

	_, cleanMessages, jsonData, err := p.prepareChatCompletionRequest(messages, tools)
	if err != nil {
		return nil, err
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
				return nil, ctx.Err()
			case <-streamCtx.Done():
				if timedOut.Load() && p.Config != nil && p.Config.Timeout > 0 {
					return nil, fmt.Errorf("LLM request timed out after %d seconds: %w", p.Config.Timeout, context.DeadlineExceeded)
				}
				return nil, streamCtx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
			kickTimer()
		}

		req, err := http.NewRequestWithContext(streamCtx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("loop", "v1.0.0")

		if p.Config.ApiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.Config.ApiKey))
		}

		var doErr error
		resp, doErr = client.Do(req)
		if doErr != nil {
			if err := p.checkContextOrTimeout(ctx, streamCtx, &timedOut, "HTTP request failed: "); err != nil {
				return nil, err
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
		kickTimer()
		break
	}

	if lastErr != nil {
		if err := p.checkContextOrTimeout(ctx, streamCtx, &timedOut, ""); err != nil {
			return nil, err
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
	setupFilterCallbacks := func(filter *FallbackToolTextFilter) {
		filter.SetToolCallbacks(
			func(toolName string, idx int) {
				normName := tool.NormalizeName(toolName)
				emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: idx})
			},
			func(chunk string, idx int) {
				emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: chunk, ToolCallIndex: idx})
			},
		)
	}

	textFilter := NewFallbackToolTextFilter(func(text string) {
		textBuilder.WriteString(text)
		emitChunk(ctx, chunkChan, StreamChunk{Type: "text", Content: text})
	})
	setupFilterCallbacks(textFilter)
	emitText := func(text string) {
		if text == "" {
			return
		}
		rawTextBuilder.WriteString(text)
		textFilter.Write(text)
	}
	reasoningFilter := NewFallbackToolTextFilter(func(text string) {
		cleanReasoningBuilder.WriteString(text)
		emitChunk(ctx, chunkChan, StreamChunk{Type: "reasoning", Content: text})
	})
	setupFilterCallbacks(reasoningFilter)
	emitReasoning := func(text string) {
		if text == "" {
			return
		}
		reasoningBuilder.WriteString(text)
		reasoningFilter.Write(text)
	}

	var promptTokens, completionTokens int
	var generationStart time.Time

	thinkingFilter := transportllm.NewThinkingStreamFilter(emitText, emitReasoning)
	var chunk ChatCompletionResponseChunk

	finalizePartial := func() *db.Message {
		thinkingFilter.Flush()
		textFilter.Flush()
		reasoningFilter.Flush()

		calls := assembleToolCalls(toolCallsMap, rawTextBuilder.String(), reasoningBuilder.String(), p.Config != nil && p.Config.ParallelToolCalls)
		pTokens, cTokens := EstimateFallbackTokens(promptTokens, completionTokens, cleanMessages, calls, rawTextBuilder.String(), reasoningBuilder.String())

		return &db.Message{
			Role:             "assistant",
			Content:          textBuilder.String(),
			ReasoningContent: StripFallbackToolMarkup(cleanReasoningBuilder.String()),
			PromptTokens:     pTokens,
			CompletionTokens: cTokens,
			ToolCalls:        calls,
		}
	}

	for {
		select {
		case <-ctx.Done():
			return finalizePartial(), ctx.Err()
		case <-streamCtx.Done():
			if err := p.checkStreamContextOrTimeout(ctx, streamCtx, &timedOut); err != nil {
				return finalizePartial(), err
			}
			return finalizePartial(), streamCtx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if streamErr := p.checkStreamContextOrTimeout(ctx, streamCtx, &timedOut); streamErr != nil {
				return finalizePartial(), streamErr
			}
			return nil, fmt.Errorf("error reading stream: %w", err)
		}

		kickTimer()

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
			thinkingFilter.Feed(choice.Delta.Content)
		}

		if len(choice.Delta.ToolCalls) > 0 {
			thinkingFilter.Flush()

			for _, tc := range choice.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}

				if p.Config != nil && !p.Config.ParallelToolCalls && idx > 0 {
					// Drop secondary tool calls so they are never emitted to UI or executed.
					// Keep reading the stream so tool call 0 receives all its arguments and content!
					continue
				}

				existing, ok := toolCallsMap[idx]
				if !ok {
					newTC := tc
					if newTC.ID == "" {
						newTC.ID = fmt.Sprintf("call_%d_%s", idx, db.NewUUID()[:8])
					}
					if newTC.Function.Name != "" {
						newTC.Function.Name = tool.NormalizeName(newTC.Function.Name)
					}
					toolCallsMap[idx] = &newTC
					if newTC.Function.Name != "" {
						emitChunk(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: newTC.Function.Name, ToolCallIndex: idx})
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

	thinkingFilter.Flush()
	textFilter.Flush()
	reasoningFilter.Flush()

	var duration time.Duration
	if !generationStart.IsZero() {
		duration = time.Since(generationStart)
	}

	calls := assembleToolCalls(toolCallsMap, rawTextBuilder.String(), reasoningBuilder.String(), p.Config != nil && p.Config.ParallelToolCalls)

	promptTokens, completionTokens = EstimateFallbackTokens(promptTokens, completionTokens, messages, calls, rawTextBuilder.String(), reasoningBuilder.String())

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

func (p *OpenAICompatibleProvider) checkContextOrTimeout(ctx, streamCtx context.Context, timedOut *atomic.Bool, prefix string) error {
	if (timedOut.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded)) && p.Config != nil && p.Config.Timeout > 0 {
		return fmt.Errorf("%sLLM request timed out after %d seconds: %w", prefix, p.Config.Timeout, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}

func (p *OpenAICompatibleProvider) checkStreamContextOrTimeout(ctx, streamCtx context.Context, timedOut *atomic.Bool) error {
	if (timedOut.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded)) && p.Config != nil && p.Config.Timeout > 0 {
		return fmt.Errorf("LLM stream timed out after %d seconds: %w", p.Config.Timeout, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}
