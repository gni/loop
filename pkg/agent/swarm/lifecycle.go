package swarm

func (mam *MultiAgentManager) SetSessionDirectory(directory string, ephemeral bool) error {
	directory, err := resolveAgentsDir(directory)
	if err != nil {
		return err
	}
	mam.mu.Lock()
	defer mam.mu.Unlock()
	mam.agentsDir = directory
	mam.ephemeral = ephemeral
	mam.generation++
	return nil
}

func (mam *MultiAgentManager) Close() {
	mam.mu.Lock()
	mam.closed = true
	mam.generation++
	agents := make([]*MultiAgent, 0, len(mam.Agents))
	for _, child := range mam.Agents {
		agents = append(agents, child)
	}
	mam.mu.Unlock()
	for _, child := range agents {
		child.stop()
	}
}

func (ma *MultiAgent) stop() {
	// An in-flight save finishes before shutdown returns. Later writes cannot
	// recreate removed files or publish a previous session's final state.
	ma.persistenceMu.Lock()
	ma.stopped = true
	ma.persistenceMu.Unlock()
	ma.CancelActiveTurn()
	if ma.Cancel != nil {
		ma.Cancel()
	}
	ma.KillTasks()
}
