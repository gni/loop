package swarm

import (
	"sort"

	"loop/pkg/agent"
	"loop/pkg/domain/message"
)

// GetSubagentsCompletionTokens returns the sum of completion tokens across all managed subagents.
func (mam *MultiAgentManager) GetSubagentsCompletionTokens() int {
	if mam == nil {
		return 0
	}
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	total := 0
	for _, ma := range mam.Agents {
		if ma != nil {
			ma.HistoryMu.RLock()
			_, comp := agent.CalculateHistoryTokens(ma.History)
			ma.HistoryMu.RUnlock()
			total += comp
		}
	}
	return total
}

// UsageSnapshot returns independent histories in name order, without holding
// swarm locks while presentation code writes to a terminal.
func (mam *MultiAgentManager) UsageSnapshot() []message.AgentUsageSnapshot {
	if mam == nil {
		return nil
	}
	mam.mu.RLock()
	defer mam.mu.RUnlock()
	names := make([]string, 0, len(mam.Agents))
	for name := range mam.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	snapshots := make([]message.AgentUsageSnapshot, 0, len(names))
	for _, name := range names {
		ma := mam.Agents[name]
		if ma == nil {
			continue
		}
		ma.HistoryMu.RLock()
		snapshots = append(snapshots, message.AgentUsageSnapshot{Name: name, Role: ma.SystemPrompt, Messages: message.CloneAll(ma.History)})
		ma.HistoryMu.RUnlock()
	}
	return snapshots
}
