package agent

import (
	"context"

	"loop/pkg/agent/tool"
)

func (a *Agent) hasSpawnedAgents() bool {
	if a == nil {
		return false
	}
	a.SpawnedAgentsMu.RLock()
	defer a.SpawnedAgentsMu.RUnlock()
	return len(a.SpawnedAgents) > 0
}

func (a *Agent) selectInferenceTools(ctx context.Context, tools []tool.Tool, allowlist []string) []tool.Tool {
	return toolProtocolForConfig(a.configForRun(ctx)).SelectInferenceTools(a, ctx, tools, allowlist)
}
