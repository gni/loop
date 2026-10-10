package limits

import (
	"reflect"
	"testing"
	"time"
)

func TestDefaultsMatchHistoricalValues(t *testing.T) {
	d := Defaults()
	if d.GrepDefaultLimit != 100 || d.GrepMaxLimit != 500 || d.GrepMaxLineLength != 500 {
		t.Errorf("grep defaults drifted: %+v", d)
	}
	if d.GrepMaxOutputBytes != 64*1024 || d.GrepMaxFileBytes != 2*1024*1024 {
		t.Errorf("grep byte defaults drifted: %+v", d)
	}
	if d.ConsecutiveLimit != 10 || d.WindowsPerTarget != 64 || d.MaxTrackedTasks != 256 {
		t.Errorf("guard/task defaults drifted: %+v", d)
	}
	if d.TaskRetentionGrace != time.Minute || d.AuditTruncationChars != 1500 {
		t.Errorf("retention/audit defaults drifted: %+v", d)
	}
	if d.NameMaxLength != 64 || d.SkillDescMaxBytes != 2048 || d.SkillContentMaxBytes != 128*1024 {
		t.Errorf("skill defaults drifted: %+v", d)
	}
	if ReasoningBudget("medium") != 2048 || ReasoningBudget("max") != -1 {
		t.Errorf("reasoning budgets drifted: %v", Master.ReasoningTokenBudgets)
	}
}

func TestResolveClampsToSafetyCeilings(t *testing.T) {
	got := Resolve(Limits{
		GrepMaxLimit:      100000, // above ceiling
		GrepMaxFileBytes:  1 << 30,
		WindowsPerTarget:  100000,
		MaxTrackedTasks:   1 << 20,
		ConsecutiveLimit:  1,      // would disable the guard
		MaxSubagentDepth:  100,
		SkillContentMaxBytes: 1 << 24,
		TaskRetentionGrace:    10 * time.Hour,
	})
	if got.GrepMaxLimit != AbsoluteMaxGrepLimit {
		t.Errorf("grep limit not clamped: %d", got.GrepMaxLimit)
	}
	if got.GrepMaxFileBytes != AbsoluteMaxGrepFileBytes {
		t.Errorf("grep file size not clamped: %d", got.GrepMaxFileBytes)
	}
	if got.WindowsPerTarget != AbsoluteMaxWindowsPerTarget {
		t.Errorf("windows not clamped: %d", got.WindowsPerTarget)
	}
	if got.MaxTrackedTasks != AbsoluteMaxTrackedTasks {
		t.Errorf("tasks not clamped: %d", got.MaxTrackedTasks)
	}
	if got.ConsecutiveLimit != AbsoluteMinConsecutiveLimit {
		t.Errorf("guard threshold must be clamped up to %d, got %d", AbsoluteMinConsecutiveLimit, got.ConsecutiveLimit)
	}
	if got.MaxSubagentDepth != AbsoluteMaxSubagentDepth {
		t.Errorf("depth not clamped: %d", got.MaxSubagentDepth)
	}
	if got.SkillContentMaxBytes != AbsoluteMaxSkillContentBytes {
		t.Errorf("skill content not clamped: %d", got.SkillContentMaxBytes)
	}
	if got.TaskRetentionGrace != AbsoluteMaxTaskRetention {
		t.Errorf("retention not clamped: %v", got.TaskRetentionGrace)
	}
}

func TestResolveKeepsDefaultsForZeroValues(t *testing.T) {
	got := Resolve(Limits{})
	if !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("zero overrides must keep defaults: %+v", got)
	}
	tight := Resolve(Limits{GrepMaxLimit: 25, ConsecutiveLimit: 3})
	if tight.GrepMaxLimit != 25 || tight.ConsecutiveLimit != 3 {
		t.Errorf("tightening must be honoured: %+v", tight)
	}
	if tight.GrepDefaultLimit > tight.GrepMaxLimit {
		t.Errorf("default limit %d must not exceed max %d", tight.GrepDefaultLimit, tight.GrepMaxLimit)
	}
}

func TestReasoningBudgetOverride(t *testing.T) {
	Set(Resolve(Limits{ReasoningTokenBudgets: map[string]int{"high": 12000}}))
	if ReasoningBudget("high") != 12000 {
		t.Errorf("override not applied: %d", ReasoningBudget("high"))
	}
	if ReasoningBudget("unknown") != DefaultReasoningBudgets()["low"] {
		t.Errorf("unknown effort must fall back to the low default")
	}
	Set(Defaults())
}

func TestClampHelperRejectsDisabledGuard(t *testing.T) {
	if ConsecutiveLimitClamped(0) != DefaultConsecutiveLimit {
		t.Errorf("zero must fall back to the default guard threshold")
	}
	if ConsecutiveLimitClamped(1) != AbsoluteMinConsecutiveLimit {
		t.Errorf("guard must never be clamped below %d", AbsoluteMinConsecutiveLimit)
	}
}
