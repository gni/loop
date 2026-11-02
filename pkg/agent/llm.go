package agent

import (
	"context"
	"sync/atomic"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
)

type contextKey string

const (
	contextKeyGenerationDuration = contextKey("generation_duration_callback")
	contextKeyChunkDropObserver  = contextKey("chunk_drop_observer")
)

type Tool = tool.Tool
type FunctionDefinition = tool.FunctionDefinition
type JSONSchema = tool.JSONSchema

type StreamChunk struct {
	Type          string
	Content       string
	ToolCallIndex int
}

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
	ParallelToolCalls    *bool               `json:"parallel_tool_calls,omitempty"`
	Temperature          *float64            `json:"temperature,omitempty"`
	FrequencyPenalty     *float64            `json:"frequency_penalty,omitempty"`
	PresencePenalty      *float64            `json:"presence_penalty,omitempty"`
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

type LLMProvider interface {
	StreamChatCompletions(
		ctx context.Context,
		messages []db.Message,
		tools []tool.Tool,
		chunkChan chan<- StreamChunk,
	) (*db.Message, error)
	CheckThinkingSupport(ctx context.Context) bool
}

type ContextLimitDetector interface {
	GetDetectedContextLimit() int
}

func (a *Agent) syncOpenAIProviderLocked(cfg *config.Config, forceReset bool) {
	switch provider := a.LLMProvider.(type) {
	case nil:
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:            cfg,
			HttpClient:        a.HttpClient,
			ChunkDropObserver: a.recordDroppedChunk,
			ResolveCredential: a.resolveCredential,
		}
	case *OpenAICompatibleProvider:
		if !forceReset && provider.Config == cfg {
			return
		}
		httpClient := provider.HttpClient
		if httpClient == nil {
			httpClient = a.HttpClient
		}
		next := &OpenAICompatibleProvider{
			Config:            cfg,
			HttpClient:        httpClient,
			ChunkDropObserver: a.recordDroppedChunk,
			ResolveCredential: a.resolveCredential,
		}
		if provider.Config != nil && cfg != nil &&
			provider.Config.Endpoint == cfg.Endpoint &&
			provider.Config.ActiveProvider == cfg.ActiveProvider &&
			provider.Config.Model == cfg.Model {
			provider.thinkingMu.Lock()
			next.ThinkingSupported = provider.ThinkingSupported
			next.ThinkingSupportChecked = provider.ThinkingSupportChecked
			provider.thinkingMu.Unlock()

			provider.ContextLimitMu.RLock()
			next.DetectedContextLimit = provider.DetectedContextLimit
			next.ContextLimitChecked = provider.ContextLimitChecked
			provider.ContextLimitMu.RUnlock()
		}
		a.LLMProvider = next
	}
}

func (a *Agent) currentLLMProvider() LLMProvider {
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()
	a.syncOpenAIProviderLocked(a.Config, false)
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
	ctx, cancel := context.WithTimeout(a.Context(), timeout)
	defer cancel()
	return provider.CheckThinkingSupport(ctx)
}

func (a *Agent) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	if a.TransformContext != nil {
		transformed, err := a.TransformContext(ctx, message.CloneAll(messages))
		if err != nil {
			return nil, err
		}
		messages = transformed
	}
	provider := a.currentLLMProvider()

	var generationDuration time.Duration
	ctxWithCallback := context.WithValue(ctx, contextKeyGenerationDuration, func(d time.Duration) {
		generationDuration = d
	})

	var tools []tool.Tool
	if a.Registry != nil && (allowlist == nil || len(allowlist) > 0) {
		tools = a.Registry.GetAvailableTools(allowlist)
	}
	msg, err := provider.StreamChatCompletions(ctxWithCallback, messages, tools, chunkChan)
	a.SetLastGenerationDuration(generationDuration)
	return msg, err
}

// StreamChatCompletionsIsolated executes a streaming completion with isolated
// generation-duration and dropped-chunk accounting so concurrent subagents do
// not overwrite the primary agent's turn metrics.
func (a *Agent) StreamChatCompletionsIsolated(
	ctx context.Context,
	messages []db.Message,
	allowlist []string,
	chunkChan chan<- StreamChunk,
) (*db.Message, time.Duration, int64, error) {
	if a.TransformContext != nil {
		transformed, err := a.TransformContext(ctx, message.CloneAll(messages))
		if err != nil {
			return nil, 0, 0, err
		}
		messages = transformed
	}
	provider := a.currentLLMProvider()

	var generationDuration time.Duration
	var droppedChunks atomic.Int64
	ctxWithCallbacks := context.WithValue(ctx, contextKeyGenerationDuration, func(d time.Duration) {
		generationDuration = d
	})
	ctxWithCallbacks = context.WithValue(ctxWithCallbacks, contextKeyChunkDropObserver, ChunkDropObserver(func() {
		droppedChunks.Add(1)
	}))

	var tools []tool.Tool
	if a.Registry != nil && (allowlist == nil || len(allowlist) > 0) {
		tools = a.Registry.GetAvailableTools(allowlist)
	}
	msg, err := provider.StreamChatCompletions(ctxWithCallbacks, messages, tools, chunkChan)
	return msg, generationDuration, droppedChunks.Load(), err
}

func prepareToolDefinitions(tools []tool.Tool, compact bool) []tool.Tool {
	return tool.PrepareToolDefinitions(tools, compact)
}

func EnforceToolPairingInvariance(messages []db.Message) []db.Message {
	return message.EnforceToolPairingInvariance(messages)
}
