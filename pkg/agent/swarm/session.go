package swarm

import (
 "io"
 "loop/pkg/ui/style"
)

func(m *MultiAgentManager)SetOutput(writer io.Writer,theme style.UITheme){
 m.mu.Lock();defer m.mu.Unlock()
 m.w=writer;m.theme=theme
}

func(m *MultiAgentManager)SwitchSessionDirectory(directory string,ephemeral bool)error{
 m.Close()
 m.mu.Lock()
 m.Agents=make(map[string]*MultiAgent)
 m.ActiveAgent=nil
 m.agentsDir=directory
 m.ephemeral=ephemeral
 m.BaseAgent.Registry.UnregisterPrefix("subagent__")
 m.BaseAgent.SpawnedAgentsMu.Lock()
 clear(m.BaseAgent.SpawnedAgents)
 m.BaseAgent.SpawnedAgentsMu.Unlock()
 m.mu.Unlock()
 return m.LoadSavedAgents()
}
