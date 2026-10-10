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
	transporthttp "loop/pkg/transport/http"
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

	// thinkingMu guards ThinkingSupported/ThinkingSupportChecked: concurrent subagent
	// loops and the UI commands layer read them while the probe writes them.
	thinkingMu sync.Mutex

	// ChunkDropObserver is notified for every stream chunk that cannot be delivered to
	// the UI consumer within streamSendTimeout, so dropped output is observable rather
	// than silently lost.
	ChunkDropObserver ChunkDropObserver
}

// ThinkingEnabled reports whether the backend advertises reasoning support, under the
// capability lock.
func (p *OpenAICompatibleProvider) ThinkingEnabled() bool {
	p.thinkingMu.Lock()
	defer p.thinkingMu.Unlock()
	return p.ThinkingSupported
}

// ThinkingProbeChecked reports whether the capability probe has already run.
func (p *OpenAICompatibleProvider) ThinkingProbeChecked() bool {
	p.thinkingMu.Lock()
	defer p.thinkingMu.Unlock()
	return p.ThinkingSupportChecked
}

// ResetThinkingCapabilities clears the cached probe result (used when the provider or
// model changes, so capabilities are re-detected rather than inherited).
func (p *OpenAICompatibleProvider) ResetThinkingCapabilities() {
	p.thinkingMu.Lock()
	p.ThinkingSupported = false
	p.ThinkingSupportChecked = false
	p.thinkingMu.Unlock()
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
					timedOut.Store(true)
					cancelStream()
					return
				}
			}
		}()
	}

	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(p.Config.Endpoint, "/"))

	if !p.ThinkingProbeChecked() {
		p.ProbeServerCapabilities(streamCtx)
		kickTimer()
	}

	_, cleanMessages, jsonData, err := p.prepareChatCompletionRequest(messages, tools)
	if err != nil {
		return nil, err
	}

	maxRetries := 3
	client := p.HttpClient
	if client == nil {
		client = http.DefaultClient
	}

	// One retry implementation instead of a hand-rolled duplicate of the resilient
	// client: the shared client now carries the stream watchdog through BeforeAttempt so
	// a stalled connection still reports as a timeout, and DescribeError keeps the
	// endpoint hint that the loop used to add inline.
	resilient := transporthttp.NewResilientClient(transporthttp.RetryConfig{
		MaxRetries: maxRetries,
		BaseDelay:  time.Second,
		Client:     client,
		BeforeAttempt: func(int) error {
			kickTimer()
			return p.checkContextOrTimeout(ctx, streamCtx, &timedOut, "LLM request timed out after ")
		},
		DescribeError: func(err error) error {
			return fmt.Errorf("HTTP request failed: %w. Check your endpoint (%s)", err, p.Config.Endpoint)
		},
	})

	resp, err := resilient.DoWithRetry(streamCtx, func() (*http.Request, error) {
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
		return req, nil
	})
	if err != nil {
		if ctxErr := p.checkContextOrTimeout(ctx, streamCtx, &timedOut, "LLM request timed out after "); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	kickTimer()
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
				emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: idx}, p.ChunkDropObserver)
			},
			func(chunk string, idx int) {
				emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: chunk, ToolCallIndex: idx}, p.ChunkDropObserver)
			},
		)
	}

	textFilter := NewFallbackToolTextFilter(func(text string) {
		textBuilder.WriteString(text)
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "text", Content: text}, p.ChunkDropObserver)
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
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "reasoning", Content: text}, p.ChunkDropObserver)
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
						emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: newTC.Function.Name, ToolCallIndex: idx}, p.ChunkDropObserver)
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
						emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: tcName, ToolCallIndex: idx}, p.ChunkDropObserver)
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.Function.Arguments != "" {
					emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: tc.Function.Arguments, ToolCallIndex: idx}, p.ChunkDropObserver)
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
	ctxVal := ctx.Value(contextKeyGenerationDuration)
	if callback, ok := ctxVal.(func(time.Duration)); ok {
		callback(duration)
	}

	return assistantMsg, nil
}

// checkContextOrTimeout is the single context/timeout classifier. `prefix` is the full
// message stem (e.g. "LLM request timed out after "); the stream path reuses it rather
// than keeping a duplicate copy.
func (p *OpenAICompatibleProvider) checkContextOrTimeout(ctx, streamCtx context.Context, timedOut *atomic.Bool, prefix string) error {
	if (timedOut.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded)) && p.Config != nil && p.Config.Timeout > 0 {
		return fmt.Errorf("%s%d seconds: %w", prefix, p.Config.Timeout, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}

// checkStreamContextOrTimeout is the stream-path wrapper of checkContextOrTimeout;
// the two were previously byte-identical copies that could drift.
func (p *OpenAICompatibleProvider) checkStreamContextOrTimeout(ctx, streamCtx context.Context, timedOut *atomic.Bool) error {
	return p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM stream timed out after ")
}
