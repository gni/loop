package agent

import (
	"sort"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/message"
)

// ContextBudget describes the last preflight allocation. Breakdown values are
// conservative estimates; Input is exact only when Measured is true.
type ContextBudget struct {
	Limit          int  `json:"limit"`
	Input          int  `json:"input"`
	Output         int  `json:"output"`
	Headroom       int  `json:"headroom"`
	EstimatedInput int  `json:"estimated_input"`
	System         int  `json:"system_estimate"`
	Tools          int  `json:"tools_estimate"`
	Task           int  `json:"task_estimate"`
	Evidence       int  `json:"evidence_estimate"`
	History        int  `json:"history_estimate"`
	Removed        int  `json:"removed_messages,omitempty"`
	Measured       bool `json:"measured"`
}

type contextUnit struct {
	start, end, tokens int
	priority           int
	required           bool
}

func isInterruptedToolMessage(msg db.Message) bool {
	if msg.ToolOutcome == evidence.Unknown || msg.ToolOutcome == evidence.NotExecuted {
		return true
	}
	if msg.Evidence != nil && (msg.Evidence.Outcome == evidence.Unknown || msg.Evidence.Outcome == evidence.NotExecuted) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(msg.Content))
	return strings.HasPrefix(lower, "error: tool execution interrupted") ||
		strings.HasPrefix(lower, "[tool result unavailable after interruption")
}

func contextUnits(messages []db.Message) []contextUnit {
	return contextUnitsFor(messages, false)
}

func contextUnitsFor(messages []db.Message, includeReasoning bool) []contextUnit {
	latestInput, latestAssistant, latestStep := -1, -1, -1
	matches := message.MatchToolResults(messages)
	latestFiles := make(map[string]int)
	for index, msg := range messages {
		if msg.Role == "tool" {
			call := matches[index].Call
			name := tool.NormalizeName(call.Function.Name)
			if name == "read" || name == "edit" || name == "write" {
				if path := filetool.ExtractFilePath(call.Function.Arguments); path != "" {
					latestFiles[path] = index
				}
			}
		}
		if msg.IsUserInput() {
			latestInput = index
		}
		if msg.Role == "assistant" {
			latestAssistant = index
		}
		if msg.Role == "assistant" || msg.Role == "tool" || msg.IsUserInput() {
			latestStep = index
		}
	}
	latestAssistantInput := -1
	if latestAssistant > 0 && messages[latestAssistant-1].IsUserInput() {
		latestAssistantInput = latestAssistant - 1
	}
	var units []contextUnit
	for start := 0; start < len(messages); {
		end := start + 1
		if messages[start].Role == "assistant" && len(messages[start].ToolCalls) > 0 {
			for end < len(messages) && messages[end].Role == "tool" {
				end++
			}
		}
		unit := contextUnit{start: start, end: end}
		hasTool := false
		allSuperseded := true
		for index := start; index < end; index++ {
			msg := messages[index]
			unit.tokens += EstimateMessageTokensWithReasoning(msg, includeReasoning)
			if msg.Role == "tool" || len(msg.ToolCalls) > 0 {
				unit.priority = max(unit.priority, 1)
				if msg.Role == "tool" {
					hasTool = true
					if msg.ContextKind != "superseded" && !strings.HasPrefix(msg.Content, "[Historical read") {
						allSuperseded = false
					}
					if path := filetool.ExtractFilePath(matches[index].Call.Function.Arguments); path != "" && latestFiles[path] == index {
						unit.priority = max(unit.priority, 2)
					}
				}
			}
			if msg.Role == "system" || isPinnedRequestContext(msg) || index == latestInput || index == latestStep || index == len(messages)-1 {
				unit.required = true
			}
			if msg.Role == "tool" && msg.ContextKind != "resolved_error" && historicalToolFailed(msg) {
				if index > latestInput {
					unit.required = true
				} else if !isInterruptedToolMessage(msg) {
					unit.priority = max(unit.priority, 2)
				}
			}
			if index == latestAssistantInput {
				unit.priority = max(unit.priority, 3)
			}
			if index == latestAssistant {
				unit.priority = max(unit.priority, 4)
			}
		}
		if hasTool && allSuperseded && unit.priority <= 1 {
			unit.priority = -1
		}
		units = append(units, unit)
		start = end
	}
	return units
}

// allocateContext removes older narrative before execution evidence while
// preserving the latest assistant turn ahead of older tool calls and keeping
// active-task failures indivisible.
func allocateContext(messages []db.Message, limit int) []db.Message {
	return allocateContextFor(messages, limit, false)
}

func allocateContextFor(messages []db.Message, limit int, includeReasoning bool) []db.Message {
	units := contextUnitsFor(messages, includeReasoning)
	total := 0
	for _, unit := range units {
		total += unit.tokens
	}
	if total <= limit {
		return messages
	}
	sort.SliceStable(units, func(i, j int) bool { return units[i].priority < units[j].priority })
	removed := make([]bool, len(messages))
	for _, unit := range units {
		if total <= limit {
			break
		}
		if unit.required {
			continue
		}
		for index := unit.start; index < unit.end; index++ {
			removed[index] = true
		}
		total -= unit.tokens
	}
	output := make([]db.Message, 0, len(messages))
	for index, msg := range messages {
		if !removed[index] {
			output = append(output, msg)
		}
	}
	return output
}

func contextBreakdown(messages []db.Message, schemaTokens int) ContextBudget {
	return contextBreakdownFor(messages, schemaTokens, false)
}

func contextBreakdownFor(messages []db.Message, schemaTokens int, includeReasoning bool) ContextBudget {
	budget := ContextBudget{Tools: schemaTokens}
	latestInput := -1
	for index, msg := range messages {
		if msg.IsUserInput() {
			latestInput = index
		}
	}
	for index, msg := range messages {
		tokens := EstimateMessageTokensWithReasoning(msg, includeReasoning)
		switch {
		case msg.Role == "system" || msg.ContextKind == "system":
			budget.System += tokens
		case isPinnedRequestContext(msg) || index == latestInput:
			budget.Task += tokens
		case msg.Role == "tool" || len(msg.ToolCalls) > 0:
			budget.Evidence += tokens
		default:
			budget.History += tokens
		}
	}
	budget.EstimatedInput = budget.System + budget.Tools + budget.Task + budget.Evidence + budget.History
	return budget
}

func (p *OpenAICompatibleProvider) setContextBudget(budget ContextBudget) {
	p.contextBudgetMu.Lock()
	p.contextBudget = &budget
	p.contextBudgetMu.Unlock()
}

func (p *OpenAICompatibleProvider) clearContextBudget() {
	p.contextBudgetMu.Lock()
	p.contextBudget = nil
	p.contextBudgetMu.Unlock()
}

func (p *OpenAICompatibleProvider) ContextBudgetSnapshot() *ContextBudget {
	p.contextBudgetMu.RLock()
	defer p.contextBudgetMu.RUnlock()
	if p.contextBudget == nil {
		return nil
	}
	copy := *p.contextBudget
	return &copy
}

func (a *Agent) ContextBudgetSnapshot() *ContextBudget {
	if a == nil {
		return nil
	}
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if provider, ok := a.LLMProvider.(interface{ ContextBudgetSnapshot() *ContextBudget }); ok {
		return provider.ContextBudgetSnapshot()
	}
	return nil
}
