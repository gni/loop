package run

import (
	"testing"

	"loop/pkg/domain/evidence"
	"loop/pkg/domain/task"
)

func TestLedgerRequiresExecutedOutcomeToReplaceFailure(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, outcome, kind, taskID string
		want                        int
	}{
		{"skipped retry", evidence.NotExecuted, "shell", "", 1},
		{"running retry", evidence.Started, "shell", "", 1},
		{"unknown retry", evidence.Unknown, "shell", "", 1},
		{"different evidence kind", evidence.Succeeded, "tool", "", 1},
		{"different background process", evidence.Succeeded, "shell", "task_2", 1},
		{"successful rerun", evidence.Succeeded, "shell", "", 0},
		{"failed rerun remains unresolved", evidence.Failed, "shell", "", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := &taskState{}
			observeLedger(state, task.Observation{Tool: "bash", Failed: true, Evidence: &evidence.Record{Kind: "shell", Target: "go test ./...", Outcome: evidence.Failed}})
			observeLedger(state, task.Observation{Tool: "bash", Evidence: &evidence.Record{Kind: tt.kind, Target: "go test ./...", TaskID: tt.taskID, Outcome: tt.outcome}})
			if got := len(state.state.Failures); got != tt.want {
				t.Fatalf("retained failures = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLedgerDistinguishesBackgroundProcesses(t *testing.T) {
	t.Parallel()
	state := &taskState{}
	for _, id := range []string{"task_1", "task_2"} {
		observeLedger(state, task.Observation{Tool: "bash", Evidence: &evidence.Record{
			Kind: "shell", Target: "go test ./...", TaskID: id, Outcome: evidence.Started,
		}})
	}
	if state.progress != 2 {
		t.Fatalf("distinct process observations advanced progress %d times, want 2", state.progress)
	}
}
