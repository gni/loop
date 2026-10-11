package agent

import (
	"testing"
)

func TestToolApprovalClassification(t *testing.T) {
	inspectionExpected := []string{
		"todo",
		"ask_user",
		"read",
		"grep",
		"find",
		"list",
		"ls",
		"load_skill",
		"task_status",
	}

	for _, name := range inspectionExpected {
		if NeedsApproval(name) {
			t.Errorf("tool %q should NOT require approval (inspection tool)", name)
		}
		if !IsInspectionTool(name) {
			t.Errorf("tool %q should report as inspection tool", name)
		}
	}

	actionExpected := []string{
		"bash",
		"write",
		"edit",
		"task_kill",
		"remove_subagent",
	}

	for _, name := range actionExpected {
		if !NeedsApproval(name) {
			t.Errorf("tool %q SHOULD require approval (action tool)", name)
		}
		if !IsActionTool(name) {
			t.Errorf("tool %q should report as action tool", name)
		}
	}
}
