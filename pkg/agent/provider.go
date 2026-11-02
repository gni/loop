package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loop/pkg/agent/fallback"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	transporthttp "loop/pkg/transport/http"
	transportllm "loop/pkg/transport/llm"
)

type OpenAICompatibleProvider struct {
	ResolveCredential      func(context.Context) (string, error)
	Config                 *config.Config
	HttpClient             *http.Client
	ThinkingSupported      bool
	ThinkingSupportChecked bool
	DetectedContextLimit   int
	ContextLimitChecked    bool
	ContextLimitMu         sync.RWMutex

	thinkingMu        sync.Mutex
	probeMu           sync.Mutex
	ChunkDropObserver ChunkDropObserver
}

func (p *OpenAICompatibleProvider) chunkDropObserver(ctx context.Context) ChunkDropObserver {
	if ctx != nil {
		if obs, ok := ctx.Value(contextKeyChunkDropObserver).(ChunkDropObserver); ok && obs != nil {
			return obs
		}
	}
	return p.ChunkDropObserver
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
	observer := p.chunkDropObserver(ctx)
	emit := func(text string) {
		dest.WriteString(text)
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: chunkType, Content: text}, observer)
	}
	if p.Config == nil || !strings.EqualFold(p.Config.ToolProtocol, "xml") {
		return fallback.NewPlainTextFilter(emit)
	}
	filter := NewFallbackToolTextFilter(emit)
	filter.SetToolCallbacks(
		func(toolName string, idx int) {
			normName := tool.NormalizeName(toolName)
			emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: normName, ToolCallIndex: idx}, observer)
		},
		func(chunk string, idx int) {
			emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: chunk, ToolCallIndex: idx}, observer)
		},
	)
	filter.SetToolTargetCallback(func(target string, idx int) {
		emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_target", Content: target, ToolCallIndex: idx}, observer)
	})
	return filter
}

func (p *OpenAICompatibleProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []tool.Tool,
	chunkChan chan<- StreamChunk,
) (result *db.Message, runErr error) {
	if err := p.validateCapabilities(messages); err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			result.Provider = p.Config.ActiveProvider
			result.Model = p.Config.Model
			selected := p.selectedModel()
			if selected.Pricing != nil {
				result.Cost = selected.Pricing.Cost(result.PromptTokens, result.CompletionTokens, result.CacheReadTokens, result.CacheWriteTokens)
			}
		}
	}()
	if p.ResolveCredential != nil {
		key, err := p.ResolveCredential(ctx)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, credentialContextKey{}, key)
	}
	if p.ResolveCredential == nil {
		if err := p.activeProviderAuthStatus(); err != nil {
			return nil, err
		}
	}
	if p.api() != "chat-completions" {
		return p.streamNative(ctx, messages, tools, chunkChan)
	}

	if p.Config != nil && p.Config.PromptCacheBreakpoints {
		messages = append([]db.Message(nil), messages...)
		transportllm.ApplyPromptCacheBreakpoints(messages)
	}

	requestStarted := time.Now()
	timeoutDuration := p.streamTimeout()

	streamCtx, kickTimer, timedOut, stopWatchdog := startStreamWatchdog(ctx, timeoutDuration)
	defer stopWatchdog()

	url := completionURL(p.Config.Endpoint)

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
		if profile, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
			for key, value := range profile.Headers {
				req.Header.Set(key, os.ExpandEnv(value))
			}
		}
		if key := p.requestKey(streamCtx); key != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))
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

		var calls []db.ToolCall
		if len(toolCallsMap) > 0 {
			calls = assembleToolCalls(toolCallsMap, "", "")
		} else if p.Config != nil && strings.EqualFold(p.Config.ToolProtocol, "xml") {
			calls = assembleToolCalls(nil, rawTextBuilder.String(), "")
		}

		return &db.Message{
			Role:     "assistant",
			Provider: p.Config.ActiveProvider, Model: p.Config.Model, StopReason: finishReason,
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
			return buildMessage(), err
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
			// OpenAI reports cached input in prompt_tokens_details; other
			// compatible servers may expose top-level cache token fields.
			cacheReadTokens = max(cacheReadTokens, chunk.Usage.CacheReadTokens, chunk.Usage.PromptTokensDetails.CachedTokens)
			cacheWriteTokens = max(cacheWriteTokens, chunk.Usage.CacheWriteTokens, chunk.Usage.PromptTokensDetails.CacheWriteTokens)
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
		if (choice.Delta.ReasoningContent != "" || choice.Delta.Content != "" || len(choice.Delta.ToolCalls) > 0) && generationStart.IsZero() {
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
			observer := p.chunkDropObserver(ctx)

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
					emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_name", Content: tcName, ToolCallIndex: idx}, observer)
				}
				if tc.Function.Arguments != "" {
					emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "tool_call", Content: tc.Function.Arguments, ToolCallIndex: idx}, observer)
				}
			}
		}
	}

	if finishReason == "" && !streamReader.Complete {
		return buildMessage(), fmt.Errorf("provider stream ended before a completion marker: %w", ErrIncompleteResponse)
	}
	if finishReason == "length" {
		return buildMessage(), fmt.Errorf("completion terminated with finish reason %q: %w", finishReason, ErrIncompleteResponse)
	}
	if finishReason == "content_filter" {
		return buildMessage(), fmt.Errorf("completion terminated with finish reason %q; response is incomplete", finishReason)
	}

	assistantMsg := buildMessage()
	if !hasUsableAssistantResponse(assistantMsg) {
		return assistantMsg, fmt.Errorf("model server closed the stream after %s without a usable answer or tool call (HTTP %d, finish reason %q): %w", time.Since(requestStarted).Round(time.Second), resp.StatusCode, finishReason, ErrEmptyResponse)
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

func completionURL(endpoint string) string {
	base := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

type credentialContextKey struct{}

func (p *OpenAICompatibleProvider) requestKey(ctx context.Context) string {
	if key, ok := ctx.Value(credentialContextKey{}).(string); ok {
		return key
	}
	return p.Config.ApiKey
}
