package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

const (
	nonFileToolMaxChars         = 30000
	nonFileToolTruncateNotice   = "\n... (output truncated to 30000 chars for context optimization)"
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
							normalisedTCs[i].Function.Arguments = "{}"
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
			totalChars += 2 + len(msg.Content)
			continue
		}

		validMessages = append(validMessages, msg)
		totalChars += len(msg.Content)
	}

	return validMessages, totalChars
}

func trimMessagesToCharBudget(validMessages []db.Message, totalChars, limitChars int) []db.Message {
	startIndex := 0
	for startIndex < len(validMessages) && validMessages[startIndex].Role == "system" {
		startIndex++
	}
	prefixCount := startIndex

	for totalChars > limitChars && startIndex < len(validMessages)-1 {
		dropEnd := startIndex + 1
		if validMessages[startIndex].Role == "assistant" && len(validMessages[startIndex].ToolCalls) > 0 {
			for dropEnd < len(validMessages)-1 && validMessages[dropEnd].Role == "tool" {
				dropEnd++
			}
		}
		for k := startIndex; k < dropEnd; k++ {
			totalChars -= len(validMessages[k].Content)
		}
		startIndex = dropEnd
	}

	if startIndex <= prefixCount {
		return validMessages
	}

	apiMessages := make([]db.Message, 0, prefixCount+len(validMessages)-startIndex)
	apiMessages = append(apiMessages, validMessages[:prefixCount]...)
	if startIndex < len(validMessages) {
		apiMessages = append(apiMessages, validMessages[startIndex:]...)
	}
	return apiMessages
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
	}

	autoAdapt := p.Config == nil || p.Config.AutoAdaptContext
	inputMsgs := messages
	if autoAdapt {
		inputMsgs = CompactHistoricalToolOutputs(inputMsgs)
	}

	validMessages, totalChars := sanitizeRequestMessages(inputMsgs)

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
	apiMessages := trimMessagesToCharBudget(validMessages, totalChars, effectiveLimit*4)
	cleanMessages := EnforceToolPairingInvariance(apiMessages)

	finalTools := prepareToolDefinitions(tools, p.Config != nil && p.Config.CompactPrompt)
	if p.Config != nil && strings.EqualFold(strings.TrimSpace(p.Config.ToolProtocol), "xml") {
		finalTools = nil
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

	maxCompTokens := limits.DefaultMaxCompletionTokens
	if p.Config != nil && p.Config.MaxCompletionTokens > 0 {
		maxCompTokens = p.Config.MaxCompletionTokens
	}
	finalPromptTokensEst := EstimateMessagesTokens(cleanMessages)
	if remainingTokens := effectiveLimit - finalPromptTokensEst; remainingTokens > 0 && maxCompTokens > remainingTokens {
		maxCompTokens = remainingTokens
	}
	if maxCompTokens < limits.CompletionTokenFloor() {
		maxCompTokens = limits.CompletionTokenFloor()
	}
	if budget > maxCompTokens && maxCompTokens > 0 {
		budget = maxCompTokens
	}

	var model string
	var temperature float64
	if p.Config != nil {
		model = p.Config.Model
		temperature = p.Config.Temperature
	}

	reqBody := ChatCompletionRequest{
		Model:               model,
		Messages:            cleanMessages,
		Tools:               finalTools,
		Temperature:         temperature,
		Stream:              true,
		StreamOptions:       &StreamOptions{IncludeUsage: true},
		ReasoningEffort:     reasoningEffort,
		MaxCompletionTokens: maxCompTokens,
		MaxTokens:           maxCompTokens,
	}

	if p.Config != nil {
		if p.Config.FrequencyPenalty != 0 {
			fp := p.Config.FrequencyPenalty
			reqBody.FrequencyPenalty = &fp
		}
		if p.Config.PresencePenalty != 0 {
			pp := p.Config.PresencePenalty
			reqBody.PresencePenalty = &pp
		}
	}

	if p.ThinkingEnabled() {
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
		parallelCalls := p.Config != nil && p.Config.ParallelToolCalls
		reqBody.ParallelToolCalls = &parallelCalls
	}

	jsonData, err := json.Marshal(reqBody)
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