package tool

import (
 "fmt"
	domaintool "loop/pkg/domain/tool"
)

type ToolResult = domaintool.ToolResult

func (r *ToolRegistry) ExecuteResult(ctx AgentContext, name, arguments string) (ToolResult, error) {
	executor, prepared, err := r.Prepare(name, arguments)
	if err != nil {
		return ToolResult{}, err
	}
	return ExecutePreparedResult(ctx, executor, prepared)
}

// ExecutePreparedResult executes arguments already normalized and validated by Prepare.
func ExecutePreparedResult(ctx AgentContext, executor ToolExecutor, arguments string) (ToolResult, error) {
	if rich, ok := executor.(domaintool.ResultExecutor); ok {
		result, err := rich.ExecuteResult(ctx, arguments)
		if result.IsError && err == nil {
			err = fmt.Errorf("tool %s reported an error", executor.Name())
		}
		result.IsError = err != nil
		return result, err
	}
	output, err := executor.Execute(ctx, arguments)
	return ToolResult{Output: output, IsError: err != nil}, err
}

func (r *ToolRegistry) CanParallel(name string) bool {
	executor, ok := r.Lookup(name)
	if !ok {
		return false
	}
	if policy, ok := executor.(interface{ ExecutionMode() string }); ok {
		return policy.ExecutionMode() == "parallel"
	}
	switch NormalizeName(name) {
	case "read", "grep", "find", "list", "task_status", "list_subagents", "audit_subagent":
		return true
	}
	return false
}
