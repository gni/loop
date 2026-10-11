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

func TestTruncationDefaultsMatchPiContract(t *testing.T) {
	d := Defaults()
	if d.MaxOutputLines != 2000 || d.MaxOutputBytes != 50*1024 {
		t.Errorf("two-limit defaults drifted: %+v", d)
	}
	if d.ReadMaxLineLength != 1000 || d.MaxReadFileBytes != 500*1024 {
		t.Errorf("read defaults drifted: %+v", d)
	}
	if d.FindMaxResults != 500 || d.ListMaxEntries != 150 || d.ListMaxDepth != 3 {
		t.Errorf("search defaults drifted: %+v", d)
	}
	if d.ParallelToolWorkers != 4 {
		t.Errorf("worker default drifted: %d", d.ParallelToolWorkers)
	}
	if d.CompactionReserveTokens != 16384 || d.CompactionKeepRecentTokens != 20000 {
		t.Errorf("compaction defaults drifted: %+v", d)
	}
}

func TestTruncationOverridesAreConfigurableButClamped(t *testing.T) {
	// A config file may lower a budget.
	lowered := Resolve(Limits{
		MaxOutputLines:   50,
		MaxOutputBytes:   2048,
		ReadMaxLineLength: 120,
		FindMaxResults:   25,
		ListMaxEntries:   40,
		ListMaxDepth:     2,
		ParallelToolWorkers: 2,
		CompactionReserveTokens: 4096,
	})
	if lowered.MaxOutputLines != 50 || lowered.MaxOutputBytes != 2048 || lowered.ReadMaxLineLength != 120 {
		t.Errorf("lowered overrides not applied: %+v", lowered)
	}
	if lowered.FindMaxResults != 25 || lowered.ListMaxEntries != 40 || lowered.ListMaxDepth != 2 {
		t.Errorf("lowered search overrides not applied: %+v", lowered)
	}
	if lowered.ParallelToolWorkers != 2 || lowered.CompactionReserveTokens != 4096 {
		t.Errorf("lowered concurrency/compaction overrides not applied: %+v", lowered)
	}

	// A config file may never raise a budget past the safety ceiling: an
	// unbounded scanner or an unbounded read is not representable.
	raised := Resolve(Limits{
		MaxOutputLines:        1 << 20,
		MaxOutputBytes:        1 << 30,
		MaxReadFileBytes:      1 << 30,
		ReadMaxLineLength:     1 << 20,
		FindMaxResults:        1 << 20,
		ListMaxEntries:        1 << 20,
		ListMaxDepth:          100,
		ParallelToolWorkers:   1000,
		CompactionReserveTokens: 1 << 20,
	})
	if raised.MaxOutputLines != AbsoluteMaxToolOutputLines || raised.MaxOutputBytes != AbsoluteMaxToolOutputBytes {
		t.Errorf("output ceilings not enforced: %+v", raised)
	}
	if raised.MaxReadFileBytes != AbsoluteMaxReadFileBytes || raised.ReadMaxLineLength != AbsoluteMaxLineLength {
		t.Errorf("read ceilings not enforced: %+v", raised)
	}
	if raised.FindMaxResults != AbsoluteMaxFindResults || raised.ListMaxEntries != AbsoluteMaxListEntries || raised.ListMaxDepth != AbsoluteMaxListDepth {
		t.Errorf("search ceilings not enforced: %+v", raised)
	}
	if raised.ParallelToolWorkers != AbsoluteMaxParallelWorkers || raised.CompactionReserveTokens != AbsoluteMaxCompactionTokens {
		t.Errorf("concurrency/compaction ceilings not enforced: %+v", raised)
	}

	// Accessors read the live resolved values.
	if MaxOutputLines() != Defaults().MaxOutputLines || ParallelToolWorkers() != 4 {
		t.Errorf("accessors disagree with Defaults(): %d/%d", MaxOutputLines(), ParallelToolWorkers())
	}
}
