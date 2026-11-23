package agent

import "loop/pkg/agent/tool"

func IsInspectionTool(name string) bool { return tool.PolicyForName(name, "").ReadOnly }
