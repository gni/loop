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
		case "system", "user", "assistant", "tool":
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
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				continue
			}
		} else {
			msg.Content = truncateNonFileToolContent(msg.Role, msg.Name, msg.Content, nonFileToolMaxChars, nonFileToolTruncateNotice)
			if msg.Content == "" {
				msg.Content = " "
			}
		}

		if msg.Role == "user" && len(validMessages) > 0 && validMessages[len(validMessages)-1].Role == "user" {
			lastIdx := len(validMessages) - 1
			if validMessages[lastIdx].Content == msg.Content {
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
	start := prefix
	for start < protected {
		candidate := make([]db.Message, 0, prefix+len(messages)-start)
		candidate = append(candidate, messages[:prefix]...)
		candidate = append(candidate, messages[start:]...)
		if requestTokens(candidate) <= limitTokens {
			return candidate
		}
		next := start + 1
		if messages[start].Role == "assistant" && len(messages[start].ToolCalls) > 0 {
			for next < protected && messages[next].Role == "tool" {
				next++
			}
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
		reqBody.ReasoningEffort = reasoningEffort
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

	jsonData, err := json.Marshal(reqBody)
	if err == nil {
		var payload map[string]any
		err = json.Unmarshal(jsonData, &payload)
		if err == nil {
			wire := make([]map[string]any, 0, len(reqBody.Messages))
			for _, msg := range reqBody.Messages {
				item := map[string]any{"role": msg.Role, "content": msg.ChatContent()}
				if len(msg.ToolCalls) > 0 {
					item["tool_calls"] = msg.ToolCalls
				}
				if msg.ToolCallID != "" {
					item["tool_call_id"] = msg.ToolCallID
				}
				if len(msg.CacheControl) > 0 {
					item["cache_control"] = msg.CacheControl
				}
				if msg.Name != "" {
					item["name"] = msg.Name
				}
				wire = append(wire, item)
			}
			payload["messages"] = wire
			jsonData, err = json.Marshal(payload)
		}
	}
	if err != nil {
		return reqBody, nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	return reqBody, cleanMessages, jsonData, nil
}

func truncateNonFileToolContent(role, name, content string, maxChars int, suffix string) string {
	if role == "tool" && name != "read" && name != "write" && name != "edit" && len(content) > maxChars {
		return filetool.UTF8SafePrefix(content, maxChars) + suffix
	}
	return content
}

func (p *OpenAICompatibleProvider) GetLimits() limits.Limits {
	if p.Config != nil {
		return limits.Resolve(p.Config.Limits)
	}
	return limits.Resolve(limits.Master)
}
