package swarm

import (
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

func TestRootSubagentSpawnRequiresExplicitPositiveDepth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MaxSubagentDepth = 0
	base := &agent.Agent{Config: cfg, Registry: tool.NewToolRegistry()}
	manager := NewMultiAgentManager(base, nil, style.GetTheme("plain"))
	if err := manager.SpawnAgent("worker", "Work on a task", "", nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("root could spawn while feature disabled: %v", err)
	}
	create := &CreateSubagentTool{mam: manager}
	_, err := create.Execute(nil, `{"name":"worker","system_prompt":"Do work"}`)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("create tool bypassed disable gate: %v", err)
	}
}
