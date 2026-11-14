package run

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"loop/pkg/domain/evidence"
	"loop/pkg/domain/task"
	"loop/pkg/domain/tool"
)

func ProgressVersion(ctx context.Context) uint64 {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.progress
}

// TrackPlan adopts the current plan provider after explicit user approval.
// It records plan ownership without fabricating a tool observation.
func TrackPlan(ctx context.Context) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.planTracked = true
	state.mu.Unlock()
	syncTaskPlan(ctx)
}

func syncTaskPlan(ctx context.Context) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	tracked := state.planTracked
	state.mu.Unlock()
	if !tracked {
		return
	}
	get, ok := ctx.Value(planKey{}).(func() []tool.TodoItem)
	if !ok || get == nil {
		return
	}
	items := get()
	plan := make([]task.Item, 0, len(items))
	if len(items) == 0 {
		plan = nil
	}
	for _, item := range items {
		plan = append(plan, task.Item{ID: item.ID, Description: item.Task, Status: item.Status})
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !reflect.DeepEqual(state.state.Plan, plan) {
		state.state.Plan = plan
		state.progress++
	}
}

// RestoreTask restores executor metadata only for an explicit continuation.
// The generated narrative summary never supplies ledger fields.
func RestoreTask(ctx context.Context, previous *task.State) bool {
	if !task.CanResume(previous) {
		return false
	}
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return false
	}
	state.mu.Lock()
	if task.HasWork(&state.state) {
		state.mu.Unlock()
		return false
	}
	switch strings.ToLower(strings.TrimSpace(state.state.Objective)) {
	case "continue", "next", "resume", "go on":
	default:
		state.mu.Unlock()
		return false
	}
	state.state = *task.Clone(previous)
	state.planTracked = len(previous.Plan) > 0
	state.state.Status, state.state.Source = task.Running, "runtime"
	state.reminded = false
	state.mu.Unlock()
	if instructions, _ := ctx.Value(objectiveKey{}).(*taskInstructions); instructions != nil {
		instructions.mu.Lock()
		constraints := append([]string{previous.Objective}, previous.Constraints...)
		instructions.inherited = latestInstructions(append(constraints, instructions.inherited...))
		instructions.mu.Unlock()
	}
	return true
}

func observeLedger(state *taskState, entry task.Observation) {
	r := entry.Evidence
	key := entry.Tool
	if r != nil {
		key += "\x00" + r.Kind + "\x00" + r.Target + "\x00" + r.Directory + "\x00" + r.TaskID + "\x00" + r.Digest + "\x00" + r.Revision + "\x00" + r.Outcome
		if len(r.FileRevisions) > 0 {
			encoded, _ := json.Marshal(r.FileRevisions)
			key += string(encoded)
		}
		if r.ExitCode != nil {
			key += fmt.Sprint(*r.ExitCode)
		}
		if r.Changed != nil && *r.Changed {
			if state.state.Revisions == nil {
				state.state.Revisions = make(map[string]string)
			}
			if r.Kind == "file" {
				state.state.Revisions[r.Target] = r.Revision
			}
			maps.Copy(state.state.Revisions, r.FileRevisions)
		}
	}
	if state.seenEvidence == nil {
		state.seenEvidence = make(map[string]bool)
	}
	key = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	if !state.seenEvidence[key] {
		state.progress++
		if len(state.seenEvidence) >= 1024 {
			state.seenEvidence = make(map[string]bool)
		}
		state.seenEvidence[key] = true
	}
	// Keep failed executor facts independently of the rolling observations list.
	// A queued, rejected, or indeterminate attempt cannot resolve an observed
	// failure. Only a completed execution for the same target can replace it.
	for i := len(state.state.Failures) - 1; i >= 0; i-- {
		old := state.state.Failures[i]
		if old.Tool != entry.Tool || old.Evidence == nil || r == nil || old.Evidence.Kind != r.Kind || old.Evidence.Target != r.Target || old.Evidence.Directory != r.Directory || old.Evidence.TaskID != r.TaskID ||
			(r.Outcome != evidence.Succeeded && r.Outcome != evidence.Failed) {
			continue
		}
		state.state.Failures = append(state.state.Failures[:i], state.state.Failures[i+1:]...)
	}
	if entry.Failed || r.HasErrors() {
		state.state.Failures = append(state.state.Failures, entry)
	}
}

func ledgerBrief(state *task.State) string {
	var b strings.Builder
	paths := make([]string, 0, len(state.Revisions))
	for path := range state.Revisions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths[:min(6, len(paths))] {
		revision := state.Revisions[path]
		if len(revision) > 12 {
			revision = revision[:12]
		}
		fmt.Fprintf(&b, "Changed revision: %s %s\n", path, revision)
	}
	if len(paths) > 6 {
		fmt.Fprintf(&b, "%d additional file revisions retained in task metadata.\n", len(paths)-6)
	}
	for _, failure := range state.Failures[:min(4, len(state.Failures))] {
		target := ""
		if failure.Evidence != nil {
			target = failure.Evidence.Target
		}
		fmt.Fprintf(&b, "Unresolved execution failure: %s %s\n", failure.Tool, boundedTaskText(target, 160))
	}
	return b.String()
}

func InitialStepBudget(ctx context.Context, maximum int) int {
	count := 0
	if state := TaskSnapshot(ctx); state != nil {
		count = len(state.Pending())
	}
	return min(maximum, 12+min(10, count)*6)
}
