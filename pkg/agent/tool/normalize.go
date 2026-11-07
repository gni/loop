package tool

import (
	"strings"

	domaintool "loop/pkg/domain/tool"
)

// Canonical subagent tool names (post-normalization). Single source of truth for the
// spawn gate and the inspection/action classification; previously duplicated in
// pkg/agent/intent.go and pkg/agent/swarm/agent.go.
var subagentTools = map[string]bool{
	"create_subagent":  true,
	"remove_subagent":  true,
	"list_subagents":   true,
	"inspect_subagent": true,
}

var subagentSpawnTools = map[string]bool{
	"create_subagent": true,
}

// IsSubagentTool reports whether a tool name belongs to the subagent family, including
// the per-agent delegation aliases (subagent__<name>).
func IsSubagentTool(name string) bool {
	norm := NormalizeName(name)
	return subagentTools[norm] || strings.HasPrefix(norm, "subagent__")
}

// IsSubagentInspectionTool reports the side-effect-free members of the subagent family.
func IsSubagentInspectionTool(name string) bool {
	return IsSubagentTool(name) && PolicyForName(name, "").ReadOnly
}

// IsSpawnTool reports whether a tool can spawn a subagent.
func IsSpawnTool(name string) bool {
	return subagentSpawnTools[NormalizeName(name)]
}

// NormalizeName maps common hallucinated tool names and aliases from local/quantized
// models to canonical tool names registered in the harness.
func NormalizeName(name string) string { return domaintool.NormalizeName(name) }
