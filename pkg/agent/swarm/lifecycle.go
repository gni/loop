package swarm

func (mam *MultiAgentManager) SetSessionDirectory(directory string, ephemeral bool) {
	mam.mu.Lock()
	defer mam.mu.Unlock()
	mam.agentsDir = directory
	mam.ephemeral = ephemeral
}

func (mam *MultiAgentManager) Close() {
	mam.mu.Lock()
	defer mam.mu.Unlock()
	for _, child := range mam.Agents {
		child.CancelActiveTurn()
		if child.Cancel != nil {
			child.Cancel()
		}
		child.KillTasks()
	}
}
