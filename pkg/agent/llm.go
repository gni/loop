package agent

import (
	"context"
	"sync/atomic"
	"time"

	"loop/pkg/agent/run"
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

type ChatCompletionRequest = chatCompletionRequest[db.Message]

// The in-memory and wire requests share every field except message representation.
type chatCompletionRequest[M any] struct {
	Model                string              `json:"model"`
	Messages             []M                 `json:"messages"`
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

func (a *Agent) compatibleProvider(cfg *config.Config, previous *OpenAICompatibleProvider) *OpenAICompatibleProvider {
	client := a.HTTPClient
	if previous != nil && previous.HTTPClient != nil {
		client = previous.HTTPClient
	}
	next := &OpenAICompatibleProvider{Config: cfg, HTTPClient: client, ChunkDropObserver: a.recordDroppedChunk,
		ResolveCredential: func(ctx context.Context) (string, error) { return a.resolveCredentialForConfig(ctx, cfg) }}
	if sameInferenceTarget(previous, next) {
		copyProviderCapabilities(next, previous)
	}
	return next
}

func sameInferenceTarget(first, second *OpenAICompatibleProvider) bool {
	return first != nil && second != nil && first.Config != nil && second.Config != nil &&
		first.Config.Endpoint == second.Config.Endpoint && first.Config.Model == second.Config.Model &&
		first.Config.ActiveProvider == second.Config.ActiveProvider
}

func copyProviderCapabilities(destination, source *OpenAICompatibleProvider) {
	counter := source.requestCounter()
	destination.tokenCounterMu.Lock()
	destination.tokenCounter = counter
	destination.tokenCounterMu.Unlock()
	source.thinkingMu.Lock()
	supported, checked := source.ThinkingSupported, source.ThinkingSupportChecked
	source.thinkingMu.Unlock()
	if checked {
		destination.thinkingMu.Lock()
		destination.ThinkingSupported, destination.ThinkingSupportChecked = supported, checked
		destination.thinkingMu.Unlock()
	}
	source.ContextLimitMu.RLock()
	limit, known := source.DetectedContextLimit, source.ContextLimitChecked
	source.ContextLimitMu.RUnlock()
	if known {
		destination.ContextLimitMu.Lock()
		destination.DetectedContextLimit, destination.ContextLimitChecked = limit, known
		destination.ContextLimitMu.Unlock()
	}
}

func (a *Agent) syncOpenAIProviderLocked(cfg *config.Config, forceReset bool) {
	switch provider := a.LLMProvider.(type) {
	case nil:
		a.LLMProvider = a.compatibleProvider(cfg, nil)
	case *OpenAICompatibleProvider:
		if !forceReset && provider.Config == cfg {
			return
		}
		a.LLMProvider = a.compatibleProvider(cfg, provider)
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
	release, err := a.acquireInference(ctx)
	if err != nil {
		return false
	}
	defer release()
	return provider.CheckThinkingSupport(ctx)
}

// SnapshotRun pins inference settings and provider capabilities for one task.
// Subagents keep separate histories but use the same queue and local endpoint.
func (a *Agent) SnapshotRun(ctx context.Context) (context.Context, *config.Config, error) {
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()
	cfg, err := config.Clone(a.Config)
	if err != nil {
		return ctx, nil, err
	}
	a.syncOpenAIProviderLocked(a.Config, false)
	provider := a.LLMProvider
	if current, ok := provider.(*OpenAICompatibleProvider); ok {
		provider = a.compatibleProvider(cfg, current)
	}
	return context.WithValue(ctx, runProviderKey{}, runSnapshot{provider: provider, config: cfg}), cfg, nil
}

type runProviderKey struct{}
type runSnapshot struct {
	provider LLMProvider
	config   *config.Config
}

func (a *Agent) configForRun(ctx context.Context) *config.Config {
	if snapshot, ok := ctx.Value(runProviderKey{}).(runSnapshot); ok {
		return snapshot.config
	}
	return a.Config
}

func (a *Agent) inferenceProvider(ctx context.Context) LLMProvider {
	if snapshot, ok := ctx.Value(runProviderKey{}).(runSnapshot); ok {
		return snapshot.provider
	}
	return a.currentLLMProvider()
}

func (a *Agent) acquireInference(ctx context.Context) (func(), error) {
	cfg := a.configForRun(ctx)
	endpoint := ""
	if cfg != nil {
		endpoint = cfg.Endpoint
	}
	return run.SharedInferenceQueue(endpoint).Acquire(ctx)
}

func (a *Agent) StreamChatCompletions(ctx context.Context, messages []db.Message, allowlist []string, chunks chan<- StreamChunk) (*db.Message, error) {
	msg, duration, dropped, err := a.StreamChatCompletionsIsolated(ctx, messages, allowlist, chunks)
	a.SetLastGenerationDuration(duration)
	a.stateMu.Lock()
	a.droppedChunks += dropped
	a.lastTurnDropped += dropped
	a.stateMu.Unlock()
	return msg, err
}

// StreamChatCompletionsIsolated is the single inference entry point for all
// agents and compaction. Only local generation is serialized; tools stay free
// to execute while another agent uses the model.
func (a *Agent) StreamChatCompletionsIsolated(ctx context.Context, messages []db.Message, allowlist []string, chunks chan<- StreamChunk) (*db.Message, time.Duration, int64, error) {
	if a.TransformContext != nil {
		transformed, err := a.TransformContext(ctx, message.CloneAll(messages))
		if err != nil {
			return nil, 0, 0, err
		}
		messages = transformed
	}
	messages = taskInstructionMessages(ctx, messages)
	messages = taskContextMessages(ctx, messages)
	release, err := a.acquireInference(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	defer release()
	provider := a.inferenceProvider(ctx)
	var duration time.Duration
	var dropped atomic.Int64
	ctx = context.WithValue(ctx, contextKeyGenerationDuration, func(d time.Duration) { duration = d })
	ctx = context.WithValue(ctx, contextKeyChunkDropObserver, ChunkDropObserver(func() { dropped.Add(1) }))
	var tools []tool.Tool
	if a.Registry != nil && (allowlist == nil || len(allowlist) > 0) {
		tools = a.Registry.GetAvailableTools(allowlist)
	}
	msg, err := provider.StreamChatCompletions(ctx, messages, tools, chunks)
	a.retainProviderCapabilities(provider)
	return msg, duration, dropped.Load(), err
}

func prepareToolDefinitions(tools []tool.Tool, compact bool) []tool.Tool {
	return tool.PrepareToolDefinitions(tools, compact)
}

func EnforceToolPairingInvariance(messages []db.Message) []db.Message {
	return message.EnforceToolPairingInvariance(messages)
}

// Keep learned local-server capabilities for later tasks without sharing mutable
// per-task configuration or forcing a fresh /props probe for every subagent.
func (a *Agent) retainProviderCapabilities(provider LLMProvider) {
	learned, ok := provider.(*OpenAICompatibleProvider)
	if !ok {
		return
	}
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()
	current, ok := a.LLMProvider.(*OpenAICompatibleProvider)
	if !ok || current == learned || !sameInferenceTarget(current, learned) {
		return
	}
	copyProviderCapabilities(current, learned)
}
