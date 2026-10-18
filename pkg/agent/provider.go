package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	thinkingMu           sync.Mutex
	ChunkDropObserver    ChunkDropObserver
}

func (p *OpenAICompatibleProvider) activeProviderAuthStatus() error {
	if p.Config == nil {
		return fmt.Errorf("provider config is nil")
	}
	if p.Config.ActiveProvider != "" && p.Config.Providers != nil {
		if prof, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
			return prof.AuthStatus()
		}
	}
	return nil
}

func (p *OpenAICompatibleProvider) ThinkingEnabled() bool {
	p.thinkingMu.Lock()
	defer p.thinkingMu.Unlock()
	return p.ThinkingSupported
}

func (p *OpenAICompatibleProvider) ThinkingProbeChecked() bool {
	p.thinkingMu.Lock()
	defer p.thinkingMu.Unlock()
	return p.ThinkingSupportChecked
}

func (p *OpenAICompatibleProvider) ResetThinkingCapabilities() {
	p.thinkingMu.Lock()
	p.ThinkingSupported = false
	p.ThinkingSupportChecked = false
	p.thinkingMu.Unlock()
}

func startStreamWatchdog(ctx context.Context, timeout time.Duration) (context.Context, func(), *atomic.Bool, func()) {
	streamCtx, cancelStream := context.WithCancel(ctx)
	var timedOut atomic.Bool
	activityChan := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})

	kick := func() {
		select {
		case activityChan <- struct{}{}:
		default:
		}
	}

	if timeout > 0 {
		go func() {
			timer := time.NewTimer(timeout)
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
					timer.Reset(timeout)
				case <-timer.C:
					timedOut.Store(true)
					cancelStream()
					return
				}
			}
		}()
	}

	stop := func() {
		close(watchdogDone)
		cancelStream()
	}

	return streamCtx, kick, &timedOut, stop
}

func (p *OpenAICompatibleProvider) newStreamTextFilter(ctx context.Context, chunkChan chan<- StreamChunk, chunkType string, dest *strings.Builder) *FallbackToolTextFilter {
	filter := NewFallbackToolTextFilter(func(text string) {
		dest.WriteString(text)
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: chunkType, Content: text}, p.ChunkDropObserver)
	})
	filter.SetToolCallbacks(
		func(toolName string, idx int) {
			normName := tool.NormalizeName(toolName)
			emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: idx}, p.ChunkDropObserver)
		},
		func(chunk string, idx int) {
			emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: chunk, ToolCallIndex: idx}, p.ChunkDropObserver)
		},
	)
	filter.SetToolTargetCallback(func(target string, idx int) {
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_target", Content: target, ToolCallIndex: idx}, p.ChunkDropObserver)
	})
	return filter
}

func (p *OpenAICompatibleProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	if err := p.activeProviderAuthStatus(); err != nil {
		return nil, err
	}
	if resolved := p.Config.ResolveModelAlias(p.Config.Model); resolved != "" && resolved != p.Config.Model {
		p.Config.Model = resolved
	}

	if p.Config != nil && p.Config.PromptCacheBreakpoints {
		messages = append([]db.Message(nil), messages...)
		transportllm.ApplyPromptCacheBreakpoints(messages)
	}

	requestStarted := time.Now()
	var timeoutDuration time.Duration
	if p.Config != nil && p.Config.Timeout > 0 {
		timeoutDuration = time.Duration(p.Config.Timeout) * time.Second
	}

	streamCtx, kickTimer, timedOut, stopWatchdog := startStreamWatchdog(ctx, timeoutDuration)
	defer stopWatchdog()

	url := fmt.Sprintf("%s/v1/chat/completions", strings.TrimSuffix(p.Config.Endpoint, "/"))

	if !p.ThinkingProbeChecked() {
		p.ProbeServerCapabilities(streamCtx)
		kickTimer()
	}

	_, _, jsonData, err := p.prepareChatCompletionRequest(messages, tools)
	if err != nil {
		return nil, err
	}

	resilient := transporthttp.NewResilientClient(transporthttp.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  time.Second,
		Client:     p.HttpClient,
		BeforeAttempt: func(int) error {
			kickTimer()
			return p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM request timed out after ")
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
		if ctxErr := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM request timed out after "); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	kickTimer()
	defer resp.Body.Close()

	streamReader := transportllm.NewStreamReader(resp.Body)
	streamReader.OnActivity = kickTimer

	var textBuilder, rawTextBuilder, reasoningBuilder, cleanReasoningBuilder strings.Builder
	toolCallsMap := make(map[int]*db.ToolCall)

	textFilter := p.newStreamTextFilter(ctx, chunkChan, "text", &textBuilder)
	reasoningFilter := p.newStreamTextFilter(ctx, chunkChan, "reasoning", &cleanReasoningBuilder)

	emitText := func(text string) {
		if text != "" {
			rawTextBuilder.WriteString(text)
			textFilter.Write(text)
		}
	}
	emitReasoning := func(text string) {
		if text != "" {
			reasoningBuilder.WriteString(text)
			reasoningFilter.Write(text)
		}
	}

	var promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int
	var generationStart time.Time
	var finishReason string

	thinkingFilter := transportllm.NewThinkingStreamFilter(emitText, emitReasoning)

	buildMessage := func() *db.Message {
		thinkingFilter.Flush()
		textFilter.Flush()
		reasoningFilter.Flush()

		calls := assembleToolCalls(toolCallsMap, rawTextBuilder.String(), reasoningBuilder.String())

		return &db.Message{
			Role:             "assistant",
			Content:          textBuilder.String(),
			ReasoningContent: StripFallbackToolMarkup(cleanReasoningBuilder.String()),
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			CacheReadTokens:  cacheReadTokens,
			CacheWriteTokens: cacheWriteTokens,
			ToolCalls:        calls,
		}
	}

	for {
		chunk, done, err := streamReader.ReadNextChunk(streamCtx)
		if err != nil {
			if streamErr := p.checkContextOrTimeout(ctx, streamCtx, timedOut, "LLM stream timed out after "); streamErr != nil {
				return buildMessage(), streamErr
			}
			return nil, err
		}
		if done {
			break
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			var upstream struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(chunk.Error, &upstream) == nil && upstream.Message != "" {
				return nil, fmt.Errorf("model server returned a stream error: %s", upstream.Message)
			}
			return nil, fmt.Errorf("model server returned a stream error: %.512s", string(chunk.Error))
		}

		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens > 0 {
				promptTokens = chunk.Usage.PromptTokens
			}
			if chunk.Usage.CompletionTokens > 0 {
				completionTokens = chunk.Usage.CompletionTokens
			}
			if chunk.Usage.CacheReadTokens > 0 || chunk.Usage.CacheWriteTokens > 0 {
				cacheReadTokens = chunk.Usage.CacheReadTokens
				cacheWriteTokens = chunk.Usage.CacheWriteTokens
			}
		}
		if chunk.Timings != nil {
			if promptTokens == 0 && chunk.Timings.PromptN > 0 {
				promptTokens = chunk.Timings.PromptN
			}
			if completionTokens == 0 && chunk.Timings.PredictedN > 0 {
				completionTokens = chunk.Timings.PredictedN
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
		if (choice.Delta.ReasoningContent != "" || choice.Delta.Content != "") && generationStart.IsZero() {
			generationStart = time.Now()
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
				if idx < 0 {
					return nil, fmt.Errorf("invalid tool call index %d", idx)
				}
				existing, ok := toolCallsMap[idx]
				if !ok {
					newTC := tc
					if newTC.ID == "" {
						newTC.ID = fmt.Sprintf("call_%d_%s", idx, db.NewUUID()[:8])
					}
					toolCallsMap[idx] = &newTC
					existing = &newTC
				} else {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Type != "" {
						existing.Type = tc.Type
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.Function.Name != "" {
					tcName := tool.NormalizeName(tc.Function.Name)
					existing.Function.Name = tcName
					emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: tcName, ToolCallIndex: idx}, p.ChunkDropObserver)
				}
				if tc.Function.Arguments != "" {
					emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: tc.Function.Arguments, ToolCallIndex: idx}, p.ChunkDropObserver)
				}
			}
		}
	}

	if finishReason == "length" || finishReason == "content_filter" {
		return nil, fmt.Errorf("completion terminated with finish reason %q; response is incomplete", finishReason)
	}

	assistantMsg := buildMessage()
	if assistantMsg.Content == "" && len(assistantMsg.ToolCalls) == 0 && assistantMsg.ReasoningContent == "" {
		return nil, fmt.Errorf("model server closed the stream after %s without assistant output (HTTP %d, finish reason %q); this is not Loop's %s inactivity timeout. Check model server and proxy logs", time.Since(requestStarted).Round(time.Second), resp.StatusCode, finishReason, timeoutDuration)
	}

	var duration time.Duration
	if !generationStart.IsZero() {
		duration = time.Since(generationStart)
	}
	if callback, ok := ctx.Value(contextKeyGenerationDuration).(func(time.Duration)); ok {
		callback(duration)
	}

	return assistantMsg, nil
}

func (p *OpenAICompatibleProvider) checkContextOrTimeout(ctx, streamCtx context.Context, timedOut *atomic.Bool, prefix string) error {
	if timedOut.Load() && p.Config != nil && p.Config.Timeout > 0 {
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
