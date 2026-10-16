package swarm

import (
	"context"
	"sort"
	"time"
)

func (ma *MultiAgent) activeTurn() (context.Context, context.CancelFunc, time.Time, bool) {
	ma.HistoryMu.RLock()
	ctx := ma.ActiveContext
	cancel := ma.ActiveCancel
	started := ma.ActiveStarted
	ma.HistoryMu.RUnlock()
	if ctx == nil || cancel == nil || ctx.Err() != nil {
		return nil, nil, time.Time{}, false
	}
	return ctx, cancel, started, true
}

func (ma *MultiAgent) CancelActiveTurn() {
	if _, cancel, _, ok := ma.activeTurn(); ok {
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
		_, _, started, ok := ma.activeTurn()
		if !ok {
			continue
		}

		next := candidate{name: ma.Name, started: started, depth: ma.Depth()}
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

	_, cancel, _, ok := ma.activeTurn()
	if !ok {
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