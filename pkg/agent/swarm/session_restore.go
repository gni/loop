package swarm

import (
	"fmt"
	"sync"

	"loop/pkg/agent/subagent"
	"loop/pkg/agent/tool"
)

// SessionRestore owns validated agents until Commit publishes them or Close
// discards them. Preparation never registers tools or starts worker goroutines.
type SessionRestore struct {
	mu         sync.Mutex
	manager    *MultiAgentManager
	staged     *MultiAgentManager
	generation uint64
	committed  bool
	closed     bool
}

// PrepareSessionDirectory loads a destination without changing the active session.
func (m *MultiAgentManager) PrepareSessionDirectory(directory string, ephemeral bool) (*SessionRestore, error) {
	directory, err := resolveAgentsDir(directory)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	if err := m.checkMutationContextLocked(nil); err != nil {
		m.mu.RUnlock()
		return nil, err
	}
	staged := &MultiAgentManager{
		BaseAgent: m.BaseAgent, Agents: make(map[string]*MultiAgent),
		Supervisor: subagent.NewTaskSupervisor(), w: m.w, theme: m.theme,
		agentsDir: directory, ephemeral: ephemeral, staging: true,
	}
	prepared := &SessionRestore{manager: m, staged: staged, generation: m.generation}
	m.mu.RUnlock()
	if err := staged.LoadSavedAgents(); err != nil {
		prepared.Close()
		return nil, err
	}
	return prepared, nil
}

// Commit replaces the current agents after all destination files have loaded.
// Concurrent session changes invalidate a prepared restore.
func (r *SessionRestore) Commit() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.committed {
		return nil
	}
	if r.closed {
		return fmt.Errorf("session restore is closed")
	}
	m := r.manager
	m.mu.Lock()
	if err := m.checkMutationContextLocked(nil); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.generation != r.generation {
		m.mu.Unlock()
		return fmt.Errorf("session changed during restore")
	}
	old := m.Agents
	m.Agents, m.ActiveAgent = r.staged.Agents, nil
	m.agentsDir, m.ephemeral = r.staged.agentsDir, r.staged.ephemeral
	m.Supervisor = r.staged.Supervisor
	m.generation++
	executors := make(map[string]tool.ToolExecutor, len(m.Agents))
	m.BaseAgent.SpawnedAgentsMu.Lock()
	m.BaseAgent.SpawnedAgents = make(map[string]bool, len(m.Agents))
	for name, ma := range m.Agents {
		ma.Manager = m
		executor := subagentExecutor(ma)
		executors[executor.Name()] = executor
		m.BaseAgent.SpawnedAgents[name] = true
	}
	m.BaseAgent.SpawnedAgentsMu.Unlock()
	m.BaseAgent.Registry.ReplacePrefix("subagent__", executors)
	for _, ma := range m.Agents {
		ma.Start(m.w, m.theme)
	}
	m.mu.Unlock()
	for _, ma := range old {
		ma.stop()
	}
	r.committed, r.closed = true, true
	return nil
}

// Close releases prepared contexts unless they were committed.
func (r *SessionRestore) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.staged.Close()
		r.closed = true
	}
}
