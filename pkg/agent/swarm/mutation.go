package swarm

import (
	"fmt"

	"loop/pkg/agent/tool"
)

// checkMutationContextLocked runs at the publication boundary, after acquiring
// the manager lock. A cancelled or replaced parent cannot change a new session.
func (m *MultiAgentManager) checkMutationContextLocked(ctx tool.AgentContext) error {
	if m.closed || m.clearing {
		return fmt.Errorf("multi-agent manager is unavailable")
	}
	if m.BaseAgent == nil || m.BaseAgent.Registry == nil {
		return fmt.Errorf("multi-agent manager is not initialized")
	}
	if lifetime := m.BaseAgent.Lifetime; lifetime != nil {
		if err := lifetime.Err(); err != nil {
			return err
		}
	}
	if ctx != nil {
		if err := ctx.Context().Err(); err != nil {
			return err
		}
		if origin, ok := ctx.(*multiAgentContext); ok && m.Agents[origin.ma.Name] != origin.ma {
			return fmt.Errorf("parent agent is no longer active")
		}
	}
	return nil
}
