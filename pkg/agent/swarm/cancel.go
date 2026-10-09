package swarm

import (
	"sort"
	"time"
)

func (ma *MultiAgent) CancelActiveTurn() {
	ma.HistoryMu.RLock()
	cancel := ma.ActiveCancel
	ma.HistoryMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (mam *MultiAgentManager) ActiveSubagentName() (string, bool) {
	type candidate struct {
		name    string
		started time.Time
		depth   int
	}

	mam.mu.RLock()
	agents := make([]*MultiAgent, 0, len(mam.Agents))
	for _, ma := range mam.Agents {
		agents = append(agents, ma)
	}
	mam.mu.RUnlock()

	var current candidate
	found := false
	for _, ma := range agents {
		ma.HistoryMu.RLock()
		activeContext := ma.ActiveContext
		activeCancel := ma.ActiveCancel
		started := ma.ActiveStarted
		ma.HistoryMu.RUnlock()
		if activeContext == nil || activeCancel == nil || activeContext.Err() != nil {
			continue
		}

		depth := 0
		for parent := ma.Parent; parent != nil; parent = parent.Parent {
			depth++
		}
		next := candidate{name: ma.Name, started: started, depth: depth}
		if !found ||
			next.started.After(current.started) ||
			(next.started.Equal(current.started) && next.depth > current.depth) ||
			(next.started.Equal(current.started) && next.depth == current.depth && next.name < current.name) {
			current = next
			found = true
		}
	}

	return current.name, found
}

func (mam *MultiAgentManager) CancelSubagentTurn(name string) bool {
	mam.mu.RLock()
	ma, exists := mam.Agents[name]
	mam.mu.RUnlock()
	if !exists {
		return false
	}

	ma.HistoryMu.RLock()
	activeContext := ma.ActiveContext
	cancel := ma.ActiveCancel
	ma.HistoryMu.RUnlock()
	if activeContext == nil || cancel == nil || activeContext.Err() != nil {
		return false
	}
	cancel()
	return true
}

func (mam *MultiAgentManager) CancelAllActiveSubagents() []string {
	mam.mu.RLock()
	names := make([]string, 0, len(mam.Agents))
	for name := range mam.Agents {
		names = append(names, name)
	}
	mam.mu.RUnlock()
	sort.Strings(names)

	cancelled := make([]string, 0, len(names))
	for _, name := range names {
		if mam.CancelSubagentTurn(name) {
			cancelled = append(cancelled, name)
		}
	}
	return cancelled
}
