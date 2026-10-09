package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

func (p *OpenAICompatibleProvider) prepareChatCompletionRequest(
	messages []db.Message,
	tools []tool.Tool,
) (ChatCompletionRequest, []db.Message, []byte, error) {
	effort := strings.ToLower(strings.TrimSpace(p.Config.ReasoningEffort))
	enableThinking := p.Config.ShowThinking && effort != "off" && effort != "none"
	budget := -1
	if enableThinking {
		switch effort {
		case "low":
			budget = 512
		case "medium":
			budget = 2048
		case "high":
			budget = 8192
		case "max":
			budget = -1
		default:
			budget = 512
		}
	}

	autoAdapt := true
	if p.Config != nil {
		autoAdapt = p.Config.AutoAdaptContext
	}

	inputMsgs := messages
	if autoAdapt {
		inputMsgs = CompactHistoricalToolOutputs(inputMsgs)
	}

	var validMessages []db.Message
	totalChars := 0

	for _, msg := range inputMsgs {
		if strings.HasPrefix(msg.Content, "[user manually executed slash command:") {
			continue
		}
		// Never send internal messages (like error banners) to the LLM API
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		if msg.Role == "assistant" {
			msg.Content = StripFallbackToolMarkup(msg.Content)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var validTCs []db.ToolCall
			for _, tc := range msg.ToolCalls {
				var dummy map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &dummy); err == nil {
					validTCs = append(validTCs, tc)
				}
			}
			msg.ToolCalls = validTCs
		}
		if msg.Role == "assistant" && msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		if msg.Role != "assistant" && msg.Content == "" {
			msgCopy := msg
			msgCopy.Content = " "
			msg = msgCopy
		}
		// If consecutive user messages occur (e.g. user retrying a prompt after an error turn), merge them
		if msg.Role == "user" && len(validMessages) > 0 && validMessages[len(validMessages)-1].Role == "user" {
			lastIdx := len(validMessages) - 1
			if validMessages[lastIdx].Content == msg.Content {
				// Same message repeated, keep single copy
				continue
			}
			totalChars -= len(validMessages[lastIdx].Content)
			validMessages[lastIdx].Content += "\n\n" + msg.Content
			totalChars += len(validMessages[lastIdx].Content)
			continue
		}
		validMessages = append(validMessages, msg)
		totalChars += len(validMessages[len(validMessages)-1].Content)
		if validMessages[len(validMessages)-1].Role == "assistant" {
			totalChars += len(validMessages[len(validMessages)-1].ReasoningContent)
		}
	}

	pTokensEst := EstimateMessagesTokens(validMessages)
	serverLimit := p.GetDetectedContextLimit()
	cfgLimit := 128000
	minWindow := 32768
	if p.Config != nil {
		if p.Config.ContextWindowLimit > 0 {
			cfgLimit = p.Config.ContextWindowLimit
		}
		if p.Config.MinContextWindow > 0 {
			minWindow = p.Config.MinContextWindow
		}
	}
	effectiveLimit := GetAdaptiveContextLimit(pTokensEst, serverLimit, cfgLimit, minWindow, autoAdapt)
	limitChars := effectiveLimit * 4

	startIndex := 0
	if len(validMessages) > 0 && validMessages[0].Role == "system" {
		startIndex = 1
	}
	for totalChars > limitChars && startIndex < len(validMessages)-1 {
		dropEnd := startIndex + 1
		if validMessages[startIndex].Role == "assistant" && len(validMessages[startIndex].ToolCalls) > 0 {
			for dropEnd < len(validMessages)-1 && validMessages[dropEnd].Role == "tool" {
				dropEnd++
			}
		}
		for k := startIndex; k < dropEnd; k++ {
			dropMsg := validMessages[k]
			dropChars := len(dropMsg.Content)
			if dropMsg.Role == "assistant" {
				dropChars += len(dropMsg.ReasoningContent)
			}
			totalChars -= dropChars
		}
		startIndex = dropEnd
	}

	var apiMessages []db.Message
	if startIndex > 0 && len(validMessages) > 0 && validMessages[0].Role == "system" {
		apiMessages = append(apiMessages, validMessages[0])
		if startIndex < len(validMessages) {
			apiMessages = append(apiMessages, validMessages[startIndex:]...)
		}
	} else if startIndex > 0 && len(validMessages) > 0 {
		apiMessages = validMessages[startIndex:]
	} else {
		apiMessages = validMessages
	}

	cleanMessages := make([]db.Message, 0, len(apiMessages))
	for _, m := range apiMessages {
		msgCopy := m
		// Omit historical reasoning content from outgoing API messages; saves thousands of prompt tokens per turn.
		msgCopy.ReasoningContent = ""
		// Only truncate massive non-file tool outputs (like runaway bash logs) if they exceed 30000 chars.
		msgCopy.Content = truncateNonFileToolContent(m.Role, m.Name, m.Content, 30000, "\n... (output truncated to 30000 chars for context optimization)")
		cleanMessages = append(cleanMessages, msgCopy)
	}

	cleanMessages = EnforceToolPairingInvariance(cleanMessages)

	finalTools := prepareToolDefinitions(tools, p.Config.CompactPrompt)

	reasoningEffort := ""
	if enableThinking {
		if effort == "max" {
			reasoningEffort = "high"
		} else if effort == "low" || effort == "medium" || effort == "high" {
			reasoningEffort = effort
		}
	}

	maxCompTokens := 16384
	if p.Config != nil && p.Config.MaxCompletionTokens > 0 {
		maxCompTokens = p.Config.MaxCompletionTokens
	}
	finalPromptTokensEst := EstimateMessagesTokens(cleanMessages)
	remainingTokens := effectiveLimit - finalPromptTokensEst
	if remainingTokens > 0 && maxCompTokens > remainingTokens {
		maxCompTokens = remainingTokens
	}
	if maxCompTokens < 512 {
		maxCompTokens = 512
	}
	if budget > maxCompTokens && maxCompTokens > 0 {
		budget = maxCompTokens
	}

	reqBody := ChatCompletionRequest{
		Model:       p.Config.Model,
		Messages:    cleanMessages,
		Tools:       finalTools,
		Temperature: p.Config.Temperature,
		Stream:      true,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
		ReasoningEffort:     reasoningEffort,
		MaxCompletionTokens: maxCompTokens,
		MaxTokens:           maxCompTokens,
	}

	if p.Config != nil && p.Config.FrequencyPenalty != 0 {
		fp := p.Config.FrequencyPenalty
		reqBody.FrequencyPenalty = &fp
	}
	if p.Config != nil && p.Config.PresencePenalty != 0 {
		pp := p.Config.PresencePenalty
		reqBody.PresencePenalty = &pp
	}

	if p.ThinkingSupported {
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
		parallelCalls := false
		if p.Config != nil && p.Config.ParallelToolCalls {
			parallelCalls = true
		}
		reqBody.ParallelToolCalls = &parallelCalls
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return reqBody, nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	return reqBody, cleanMessages, jsonData, nil
}

// truncateNonFileToolContent truncates non-file tool output exceeding maxChars.
func truncateNonFileToolContent(role, name, content string, maxChars int, suffix string) string {
	if role == "tool" && name != "read" && name != "write" && name != "edit" && len(content) > maxChars {
		return content[:maxChars] + suffix
	}
	return content
}

