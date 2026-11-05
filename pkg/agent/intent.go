package agent

import "loop/pkg/agent/tool"

func IsInspectionTool(name string) bool { return tool.PolicyForName(name, "").ReadOnly }
func IsActionTool(name string) bool     { return tool.PolicyForName(name, "").RequiresPlan }
func NeedsApproval(name string) bool    { return tool.PolicyForName(name, "").RequiresApproval }
