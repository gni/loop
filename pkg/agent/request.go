package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

const (
	nonFileToolMaxChars       = 8192
	nonFileToolTruncateNotice = "\n... (historical tool output truncated to 8192 bytes for context; use spill file for full output)"
)

func sanitizeRequestMessages(inputMsgs []db.Message) ([]db.Message, int) {
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
			}
		case "user", "assistant", "tool":
		default:
			continue
		}

		msg.ReasoningContent = ""

		if msg.Role == "assistant" {
			msg.Content = StripFallbackToolMarkup(msg.Content)
			if len(msg.ToolCalls) > 0 {
				normalisedTCs := make([]db.ToolCall, len(msg.ToolCalls))
				copy(normalisedTCs, msg.ToolCalls)
				for i, tc := range normalisedTCs {
					var dummy map[string]interface{}
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
			if msg.Content == "" && len(msg.ToolCalls) == 0 && len(msg.ContentBlocks) == 0 {
				continue
			}
		} else {
			msg.Content = truncateNonFileToolContent(msg.Role, msg.Name, msg.Content, nonFileToolMaxChars, nonFileToolTruncateNotice)
			if msg.Content == "" && len(msg.ContentBlocks) == 0 {
				msg.Content = " "
			}
		}

		if msg.Role == "user" && len(validMessages) > 0 && validMessages[len(validMessages)-1].Role == "user" {
			lastIdx := len(validMessages) - 1
			if validMessages[lastIdx].Content == msg.Content && len(msg.ContentBlocks) == 0 {
				continue
			}
			validMessages[lastIdx].Content += "\n\n" + msg.Content
			validMessages[lastIdx].ContentBlocks = append(append([]message.ContentBlock(nil), validMessages[lastIdx].ContentBlocks...), msg.ContentBlocks...)
			totalChars += 2 + len(msg.Content)
			continue
		}

		validMessages = append(validMessages, msg)
		totalChars += messageChars(msg)
	}

	return validMessages, totalChars
}

// trimMessagesToTokenBudget drops the oldest turns while preserving system
// instructions and the latest message. Tool-call/result groups are dropped as
// a unit; pairing is checked again by the caller.
func trimMessagesToTokenBudget(messages []db.Message, limitTokens int) []db.Message {
	if len(messages) == 0 {
		return messages
	}
	prefix := 0
	for prefix < len(messages) && messages[prefix].Role == "system" {
		prefix++
	}
	// Never remove the assistant call that owns the latest tool results.
	// An orphan tool result would be discarded by the pairing validator.
	protected := len(messages) - 1
	if protected >= prefix && messages[protected].Role == "tool" {
		i := protected
		for i >= prefix && messages[i].Role == "tool" {
			i--
		}
		if i >= prefix && messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			protected = i
		}
	}
	msgChars := make([]int, len(messages))
	totalChars := 0
	for i, msg := range messages {
		c := messageTokenChars(msg)
		msgChars[i] = c
		totalChars += c
	}
	if TokensFromChars(totalChars) <= limitTokens {
		return messages
	}
	start := prefix
	for start < protected && TokensFromChars(totalChars) > limitTokens {
		next := start + 1
		if messages[start].Role == "assistant" && len(messages[start].ToolCalls) > 0 {
			for next < protected && messages[next].Role == "tool" {
				next++
			}
		}
		for i := start; i < next; i++ {
			totalChars -= msgChars[i]
		}
		start = next
	}
	if start == prefix {
		return messages
	}
	trimmed := make([]db.Message, 0, prefix+len(messages)-start)
	trimmed = append(trimmed, messages[:prefix]...)
	return append(trimmed, messages[start:]...)
}

// chatCapabilities never guesses support from the name of the endpoint.
func (p *OpenAICompatibleProvider) chatCapabilities() config.ChatCapabilities {
	if p.Config == nil {
		return config.ChatCapabilities{}
	}
	caps := p.Config.ChatCapabilities
	if provider, ok := p.Config.Providers[p.Config.ActiveProvider]; ok {
		caps = caps.Merge(provider.ChatCapabilities)
	}
	return caps
}

func (p *OpenAICompatibleProvider) prepareChatCompletionRequest(
	messages []db.Message,
	tools []tool.Tool,
) (ChatCompletionRequest, []db.Message, []byte, error) {
	effort := ""
	if p.Config != nil {
		effort = strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort))
	}
	enableThinking := effort != "off" && effort != "none" && effort != ""
	budget := -1
	if enableThinking {
		budget = limits.ReasoningBudget(effort)
		if p.Config != nil {
			budget = limits.Resolve(p.Config.Limits).ReasoningTokenBudgets[effort]
		}
	}

	autoAdapt := p.Config == nil || p.Config.AutoAdaptContext
	inputMsgs := messages
	if autoAdapt {
		inputMsgs = CompactHistoricalToolOutputs(inputMsgs)
	}

	validMessages, _ := sanitizeRequestMessages(inputMsgs)

	finalTools := prepareToolDefinitions(tools, p.Config != nil && p.Config.CompactPrompt)
	if p.Config != nil && strings.EqualFold(strings.TrimSpace(p.Config.ToolProtocol), "xml") {
		finalTools = nil
	}
	cleanMessages, maxCompTokens, err := p.budgetRequest(validMessages, finalTools)
	if err != nil {
		return ChatCompletionRequest{}, nil, nil, err
	}

	reasoningEffort := ""
	if enableThinking {
		switch effort {
		case "max":
			reasoningEffort = "high"
		case "low", "medium", "high":
			reasoningEffort = effort
		}
	}

	if budget > maxCompTokens && maxCompTokens > 0 {
		budget = maxCompTokens
	}

	caps := p.chatCapabilities()
	if !enableThinking && p.Config != nil && !caps.ReasoningEffort && !caps.ThinkingControls && len(p.selectedModel().Reasoning) > 0 {
		return ChatCompletionRequest{}, nil, nil, fmt.Errorf("cannot enforce reasoning:off for model %q: the provider has no configured reasoning_effort or thinking_controls capability", p.Config.Model)
	}
	var model string
	var temperature float64
	if p.Config != nil {
		model = p.Config.ResolveModelAlias(p.Config.Model)
		temperature = p.Config.Temperature
	}

	reqBody := ChatCompletionRequest{
		Model:    model,
		Messages: cleanMessages,
		Tools:    finalTools,
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
	} else {
		if caps.ParallelToolCalls {
			// Provider batching is independent of local execution concurrency.
			// The engine still decides which tool calls can run in parallel.
			parallelCalls := true
			reqBody.ParallelToolCalls = &parallelCalls
		}
	}

	wireMessages := make([]wireChatMessage, 0, len(reqBody.Messages))
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

	for _, msg := range reqBody.Messages {
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
		flushPendingImages()
		wireMessages = append(wireMessages, wireChatMessage{
			Role:         msg.Role,
			Content:      msg.ChatContent(),
			ToolCalls:    msg.ToolCalls,
			ToolCallID:   msg.ToolCallID,
			CacheControl: msg.CacheControl,
			Name:         msg.Name,
		})
	}
	flushPendingImages()
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
		return reqBody, nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	return reqBody, cleanMessages, jsonData, nil
}

type wireChatMessage struct {
	Role         string               `json:"role"`
	Content      any                  `json:"content"`
	ToolCalls    []db.ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID   string               `json:"tool_call_id,omitempty"`
	CacheControl []message.CacheBlock `json:"cache_control,omitempty"`
	Name         string               `json:"name,omitempty"`
}

type chatCompletionWireRequest struct {
	Model                string              `json:"model"`
	Messages             []wireChatMessage   `json:"messages"`
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

func truncateNonFileToolContent(role, name, content string, maxChars int, suffix string) string {
	if role == "tool" && name != "read" && name != "write" && name != "edit" && len(content) > maxChars {
		if name == "bash" {
			prefix := "... (earlier command output omitted for context limits)\n"
			if strings.Contains(suffix, "optimization") {
				prefix = "... (earlier command output omitted for context optimization)\n"
			}
			return prefix + filetool.UTF8SafeSuffix(content, maxChars)
		}
		return filetool.UTF8SafePrefix(content, maxChars) + suffix
	}
	return content
}

func (p *OpenAICompatibleProvider) GetLimits() limits.Limits {
	if p.Config != nil {
		return limits.Resolve(p.Config.Limits)
	}
	return limits.Resolve(limits.Current())
}
