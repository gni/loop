package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/storage"
	transportllm "loop/pkg/transport/llm"
)

const nonFileToolMaxBytes = 8192

func sanitizeRequestMessages(inputMsgs []db.Message) ([]db.Message, int) {
	return sanitizeRequestMessagesWithOptions(inputMsgs, false)
}

func sanitizeRequestMessagesWithOptions(inputMsgs []db.Message, preserveReasoning bool) ([]db.Message, int) {
	validMessages := make([]db.Message, 0, len(inputMsgs))
	totalChars := 0

	for _, msg := range inputMsgs {
		if strings.HasPrefix(msg.Content, "[user manually executed slash command:") {
			continue
		}
		switch msg.Role {
		case "system":
			if len(validMessages) > 0 {
				// Normalize mid-history system messages (e.g. compaction summaries)
				// to user context turns so strict OpenAI-compatible proxies and chat templates
				// (e.g. vLLM, llama.cpp, ChatML) don't reject them or break KV caching.
				msg.Role = "user"
				if msg.ContextKind == "" {
					msg.ContextKind = "system"
				}
			}
		case "user", "assistant", "tool":
		default:
			continue
		}
		msg = mergeRequestTextBlocks(msg)

		if !preserveReasoning || msg.Role != "assistant" {
			msg.ReasoningContent = ""
		}
		if msg.Role == "tool" && msg.Name != "" {
			msg.Name = tool.NormalizeName(msg.Name)
		}

		if msg.Role == "assistant" {
			msg.Content = StripFallbackToolMarkup(msg.Content)
			if len(msg.ToolCalls) > 0 {
				normalisedTCs := make([]db.ToolCall, len(msg.ToolCalls))
				copy(normalisedTCs, msg.ToolCalls)
				for i, tc := range normalisedTCs {
					normalisedTCs[i].Function.Name = tool.NormalizeName(tc.Function.Name)
					var dummy map[string]any
					if json.Unmarshal([]byte(tc.Function.Arguments), &dummy) != nil {
						repaired := domaintool.RepairJSON(tc.Function.Arguments)
						if json.Unmarshal([]byte(repaired), &dummy) == nil {
							normalisedTCs[i].Function.Arguments = repaired
						} else {
							// Keep the invalid-call diagnosis visible to the model
							// without forwarding malformed JSON to the provider.
							normalisedTCs[i].Function.Arguments = `{"_tool_call_error":"Invalid JSON tool arguments in the earlier assistant message"}`
						}
					}
				}
				msg.ToolCalls = normalisedTCs
			}
			if msg.Content == "" && len(msg.ToolCalls) == 0 && !msg.HasRenderableBlocks() {
				continue
			}
		} else {
			if msg.Content == "" && !msg.HasRenderableBlocks() {
				msg.Content = " "
			}
		}

		validMessages = append(validMessages, msg)
		totalChars += messageWireChars(msg, preserveReasoning)
	}

	return validMessages, totalChars
}

// trimMessagesToTokenBudget evicts old conversation units while preserving
// system instructions, the active task/user updates, and the latest evidence.
// Assistant calls and their tool responses are removed together.
func trimMessagesToTokenBudget(messages []db.Message, limitTokens int) []db.Message {
	return allocateContext(messages, limitTokens)
}

func trimMessagesToTokenBudgetFor(messages []db.Message, limitTokens int, includeReasoning bool) []db.Message {
	return allocateContextFor(messages, limitTokens, includeReasoning)
}

// chatCapabilities merges explicit configuration capabilities with probed server support.
func (p *OpenAICompatibleProvider) chatCapabilities() config.ChatCapabilities {
	if p.Config == nil {
		return config.ChatCapabilities{}
	}
	caps := p.Config.ChatCapabilities
	if provider, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
		caps = caps.Merge(provider.ChatCapabilities)
	}
	caps = caps.Merge(p.ProbedChatCapabilities())
	if p.ThinkingEnabled() {
		caps.ThinkingControls = true
		caps.ReasoningEffort = true
	}
	return caps
}

func (p *OpenAICompatibleProvider) prepareChatCompletionRequest(
	messages []db.Message,
	tools []tool.Tool,
) (ChatCompletionRequest, []db.Message, []byte, error) {
	return p.prepareChatCompletionRequestContext(nil, messages, tools)
}

func (p *OpenAICompatibleProvider) prepareChatCompletionRequestContext(ctx context.Context, messages []db.Message, tools []tool.Tool) (ChatCompletionRequest, []db.Message, []byte, error) {
	preserveReasoning := p.PreserveReasoningEnabled()
	validMessages, _ := sanitizeRequestMessagesWithOptions(messages, preserveReasoning)

	proto := toolProtocolForConfig(p.Config)
	finalTools := proto.PrepareWireTools(tools, p.Config != nil && p.Config.CompactPrompt)
	wireMessages := proto.PrepareWireMessages(validMessages)
	var compactFallback func() []db.Message
	if historyCompressionEnabled(p.Config) {
		compactFallback = func() []db.Message {
			compacted := CompactHistoricalToolOutputs(messages)
			valid, _ := sanitizeRequestMessagesWithOptions(compacted, preserveReasoning)
			return proto.PrepareWireMessages(valid)
		}
	}
	cleanMessages, maxCompTokens, err := p.budgetRequestContextWithFallback(ctx, wireMessages, finalTools, compactFallback)
	if err != nil {
		return ChatCompletionRequest{}, nil, nil, err
	}

	reqBody, err := p.chatCompletionRequest(cleanMessages, finalTools, maxCompTokens)
	if err != nil {
		return ChatCompletionRequest{}, nil, nil, err
	}
	if p.Config != nil && p.Config.PromptCacheBreakpoints {
		transportllm.ApplyPromptCacheBreakpoints(reqBody.Messages)
	}
	jsonData, err := marshalChatCompletionRequestWithOptions(reqBody, preserveReasoning)
	return reqBody, cleanMessages, jsonData, err
}

// normalizedReasoningEffort returns a canonical effort string ("off", "low",
// "medium", "high", "max", or "" when unset), normalizing aliases like "false"/"0".
func normalizedReasoningEffort(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	raw := strings.TrimSpace(cfg.ReasoningEffort)
	if raw == "" {
		return ""
	}
	if parsed, err := config.ParseReasoningEffort(raw); err == nil {
		return parsed
	}
	return strings.ToLower(raw)
}

// chatCompletionRequest is shared by inference and the optional local token counter
// so tools, thinking controls, and chat formatting use the same request settings.
func (p *OpenAICompatibleProvider) chatCompletionRequest(messages []db.Message, tools []tool.Tool, maxCompTokens int) (ChatCompletionRequest, error) {
	effort := normalizedReasoningEffort(p.Config)
	enableThinking := effort != "off" && effort != "none" && effort != ""
	budget := -1
	if enableThinking {
		budget = limits.ReasoningBudget(effort)
		if p.Config != nil {
			budget = limits.Resolve(p.Config.Limits).ReasoningTokenBudgets[effort]
		}
	}

	reasoningEffort := ""
	if enableThinking {
		reasoningEffort = p.wireReasoningEffort(effort)
	}

	if budget > maxCompTokens && maxCompTokens > 0 {
		budget = maxCompTokens
	}

	caps := p.chatCapabilities()
	if !enableThinking && p.Config != nil && !caps.ReasoningEffort && !caps.ThinkingControls && len(p.selectedModel().Reasoning) > 0 {
		return ChatCompletionRequest{}, fmt.Errorf("cannot enforce reasoning:off for model %q: the provider has no configured reasoning_effort or thinking_controls capability", p.Config.Model)
	}
	var model string
	var temperature float64
	if p.Config != nil {
		model = p.Config.ResolveModelAlias(p.Config.Model)
		temperature = p.Config.Temperature
	}

	reqBody := ChatCompletionRequest{
		Model:    model,
		Messages: mergeAdjacentUserMessages(messages),
		Tools:    tools,
		Stream:   true,
	}

	if caps.Temperature {
		reqBody.Temperature = &temperature
	}
	if caps.StreamOptions {
		reqBody.StreamOptions = &StreamOptions{IncludeUsage: true}
	}
	// A backend can use one token-limit field, never both in the same request.
	if caps.MaxCompletionTokens {
		reqBody.MaxCompletionTokens = maxCompTokens
	} else if caps.MaxTokens {
		reqBody.MaxTokens = maxCompTokens
	}
	if caps.ReasoningEffort {
		if enableThinking {
			reqBody.ReasoningEffort = reasoningEffort
		} else {
			// Omitting this field can leave the model's default reasoning enabled.
			reqBody.ReasoningEffort = "none"
		}
	}
	if caps.Penalties && p.Config != nil {
		if p.Config.FrequencyPenalty != 0 {
			fp := p.Config.FrequencyPenalty
			reqBody.FrequencyPenalty = &fp
		}
		if p.Config.PresencePenalty != 0 {
			pp := p.Config.PresencePenalty
			reqBody.PresencePenalty = &pp
		}
	}

	if caps.ThinkingControls {
		reqBody.ReasoningControl = true
		if enableThinking {
			strength := reasoningEffort
			reqBody.ReasoningFormat = "auto"
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking:    true,
				ReasoningEffort:   strength,
				ReasoningStrength: strength,
			}
			if budget >= 0 {
				b := budget
				reqBody.ThinkingBudgetTokens = &b
			}
		} else {
			zero := 0
			reqBody.ReasoningFormat = "none"
			reqBody.ThinkingBudgetTokens = &zero
			reqBody.ChatTemplateKwargs = &ChatTemplateKwargs{
				EnableThinking:    false,
				ReasoningEffort:   "none",
				ReasoningStrength: "none",
			}
		}
	}

	if len(reqBody.Tools) == 0 {
		reqBody.Tools = nil
	} else {
		if caps.ParallelToolCalls && p.Config != nil {
			parallelCalls := p.Config.ParallelToolCalls
			reqBody.ParallelToolCalls = &parallelCalls
		}
	}

	return reqBody, nil
}

func marshalChatCompletionRequest(reqBody ChatCompletionRequest) ([]byte, error) {
	return marshalChatCompletionRequestWithOptions(reqBody, false)
}

func buildWireChatMessages(messages []db.Message, preserveReasoning bool) []wireChatMessage {
	wireMessages := make([]wireChatMessage, 0, len(messages))
	var pendingImageParts []map[string]any

	flushPendingImages := func() {
		if len(pendingImageParts) > 0 {
			userParts := append([]map[string]any{{"type": "text", "text": "Attached image(s) from tool result:"}}, pendingImageParts...)
			wireMessages = append(wireMessages, wireChatMessage{
				Role:    "user",
				Content: userParts,
			})
			pendingImageParts = nil
		}
	}

	for _, msg := range messages {
		if msg.Role == "tool" {
			hasImages := false
			for _, block := range msg.ContentBlocks {
				if block.Type == "image" {
					hasImages = true
					url := block.URL
					if url == "" {
						url = "data:" + block.MimeType + ";base64," + block.Data
					}
					pendingImageParts = append(pendingImageParts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}})
				}
			}
			content := msg.Content
			if content == "" {
				if hasImages {
					content = "(see attached image)"
				} else {
					content = " "
				}
			}
			wireMessages = append(wireMessages, wireChatMessage{
				Role:         "tool",
				Content:      content,
				ToolCallID:   msg.ToolCallID,
				CacheControl: msg.CacheControl,
				Name:         msg.Name,
			})
			continue
		}
		if len(pendingImageParts) > 0 && msg.Role == "user" {
			userParts := append([]map[string]any{{"type": "text", "text": "Attached image(s) from tool result:"}}, pendingImageParts...)
			pendingImageParts = nil
			switch content := msg.ChatContent().(type) {
			case string:
				if content != "" {
					userParts = append(userParts, map[string]any{"type": "text", "text": content})
				}
			case []map[string]any:
				userParts = append(userParts, content...)
			}
			wireMessages = append(wireMessages, wireChatMessage{
				Role:         msg.Role,
				Content:      userParts,
				ToolCalls:    msg.ToolCalls,
				ToolCallID:   msg.ToolCallID,
				CacheControl: msg.CacheControl,
				Name:         msg.Name,
			})
			continue
		}
		flushPendingImages()
		reasoning := ""
		if preserveReasoning && msg.Role == "assistant" {
			reasoning = msg.ReasoningContent
		}
		wireMessages = append(wireMessages, wireChatMessage{
			Role:             msg.Role,
			Content:          msg.ChatContent(),
			ReasoningContent: reasoning,
			ToolCalls:        msg.ToolCalls,
			ToolCallID:       msg.ToolCallID,
			CacheControl:     msg.CacheControl,
			Name:             msg.Name,
		})
	}
	flushPendingImages()
	return wireMessages
}

func marshalChatCompletionRequestWithOptions(reqBody ChatCompletionRequest, preserveReasoning bool) ([]byte, error) {
	wireMessages := buildWireChatMessages(reqBody.Messages, preserveReasoning)
	wireReq := chatCompletionWireRequest{
		Model:                reqBody.Model,
		Messages:             wireMessages,
		Tools:                reqBody.Tools,
		ParallelToolCalls:    reqBody.ParallelToolCalls,
		Temperature:          reqBody.Temperature,
		FrequencyPenalty:     reqBody.FrequencyPenalty,
		PresencePenalty:      reqBody.PresencePenalty,
		Stream:               reqBody.Stream,
		StreamOptions:        reqBody.StreamOptions,
		ReasoningEffort:      reqBody.ReasoningEffort,
		ReasoningFormat:      reqBody.ReasoningFormat,
		ThinkingBudgetTokens: reqBody.ThinkingBudgetTokens,
		ReasoningControl:     reqBody.ReasoningControl,
		ChatTemplateKwargs:   reqBody.ChatTemplateKwargs,
		MaxCompletionTokens:  reqBody.MaxCompletionTokens,
		MaxTokens:            reqBody.MaxTokens,
	}
	jsonData, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	return jsonData, nil
}

type wireChatMessage struct {
	Role             string               `json:"role"`
	Content          any                  `json:"content"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []db.ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string               `json:"tool_call_id,omitempty"`
	CacheControl     []message.CacheBlock `json:"cache_control,omitempty"`
	Name             string               `json:"name,omitempty"`
}

type chatCompletionWireRequest = chatCompletionRequest[wireChatMessage]

func truncateNonFileToolContent(msg db.Message, maxBytes int) string {
	if msg.Role == "tool" && !historicalToolFailed(msg) && storage.IsSpillPath(msg.SpillPath) && msg.Name != "read" && msg.Name != "write" && msg.Name != "edit" && msg.Name != "ask_user" && msg.Name != "load_skill" {
		return boundedHistoricalOutput(msg.Content, msg.SpillPath, maxBytes)
	}
	return msg.Content
}

func (p *OpenAICompatibleProvider) GetLimits() limits.Limits {
	if p.Config != nil {
		return limits.Resolve(p.Config.Limits)
	}
	return limits.Resolve(limits.Current())
}
