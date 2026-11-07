package tool

import (
	"encoding/json"

	domaintool "loop/pkg/domain/tool"
)

type ExecutionPolicy = domaintool.ExecutionPolicy

var inspectionPolicy = ExecutionPolicy{ReadOnly: true, Parallel: true, RetrySafe: true}
var actionPolicy = ExecutionPolicy{RequiresApproval: true, RequiresPlan: true}

// PolicyForName is the canonical builtin effect table. State-only operations
// such as plan updates stay ordered without requiring filesystem approval.
func PolicyForName(name, arguments string) ExecutionPolicy {
	switch NormalizeName(name) {
	case "read", "grep", "find", "list", "search_symbols", "task_status", "list_subagents", "audit_subagent",
		"search_tools", "list_mcp_resources", "list_mcp_resource_templates":
		return inspectionPolicy
	case "ask_user", "load_skill":
		return ExecutionPolicy{ReadOnly: true, RetrySafe: false}
	case "todo", "task_state":
		var args struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(arguments), &args) == nil && (args.Action == "list" || args.Action == "get") {
			return ExecutionPolicy{ReadOnly: true, RetrySafe: true}
		}
		return ExecutionPolicy{}
	default:
		return actionPolicy
	}
}

func (r *ToolRegistry) Policy(name, arguments string) ExecutionPolicy {
	executor, exists := r.Lookup(name)
	if !exists {
		return actionPolicy
	}
	if provider, ok := executor.(domaintool.PolicyProvider); ok {
		return provider.ExecutionPolicy(arguments)
	}
	policy := PolicyForName(name, arguments)
	// Preserve explicit integration scheduling declarations. They do not grant
	// approval exemptions or establish that repeating the operation is safe.
	if mode, ok := executor.(interface{ ExecutionMode() string }); ok {
		policy.Parallel = mode.ExecutionMode() == "parallel"
	}
	return policy
}
