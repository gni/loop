package agent

import (
	"context"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/message"
)

type contextKey string

const contextKeyGenerationDuration = contextKey("generation_duration_callback")

type Tool = tool.Tool
type FunctionDefinition = tool.FunctionDefinition
type JSONSchema = tool.JSONSchema
type SchemaProp = tool.SchemaProp
type ReplaceEdit = tool.ReplaceEdit

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
	Temperature          float64             `json:"temperature"`
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
	Timings *struct {
		PromptN    int `json:"prompt_n"`
		PredictedN int `json:"predicted_n"`
	} `json:"timings,omitempty"`
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

func (a *Agent) currentLLMProvider() LLMProvider {
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()

	if a.LLMProvider == nil {
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:            a.Config,
			HttpClient:        a.HttpClient,
			ChunkDropObserver: a.recordDroppedChunk,
		}
	} else if provider, ok := a.LLMProvider.(*OpenAICompatibleProvider); ok && provider.Config != a.Config {
		httpClient := provider.HttpClient
		if httpClient == nil {
			httpClient = a.HttpClient
		}
		a.LLMProvider = &OpenAICompatibleProvider{
			Config:            a.Config,
			HttpClient:        httpClient,
			ChunkDropObserver: a.recordDroppedChunk,
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
	parent := a.Context()
	ctx, cancel := context.WithTimeout(parent, timeout)
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

	var generationDuration time.Duration
	ctxWithCallback := context.WithValue(ctx, contextKeyGenerationDuration, func(d time.Duration) {
		generationDuration = d
	})

	var tools []tool.Tool
	if a.Registry != nil {
		tools = a.Registry.GetAvailableTools(allowlist)
	}
	msg, err := provider.StreamChatCompletions(ctxWithCallback, messages, tools, chunkChan)

	a.SetLastGenerationDuration(generationDuration)

	return msg, err
}

func compressToolDefinition(t tool.Tool) tool.Tool {
	return tool.CompressToolDefinition(t)
}

func prepareToolDefinitions(tools []tool.Tool, compact bool) []tool.Tool {
	return tool.PrepareToolDefinitions(tools, compact)
}

func EnforceToolPairingInvariance(messages []db.Message) []db.Message {
	return message.EnforceToolPairingInvariance(messages)
}