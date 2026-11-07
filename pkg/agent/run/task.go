package run

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"loop/pkg/domain/evidence"
	"loop/pkg/domain/message"
	"loop/pkg/domain/task"
	"loop/pkg/domain/tool"
)

type taskKey struct{}
type taskState struct {
	mu       sync.Mutex
	state    task.State
	reminded bool
}

func reopenTask(ctx context.Context) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.state.Status, state.state.Source, state.state.Summary = task.Running, "runtime", ""
	state.reminded = false
}

func WithTask(ctx context.Context, id, objective string) context.Context {
	return context.WithValue(ctx, taskKey{}, &taskState{state: task.State{ID: id, Objective: objective, Status: task.Running, Source: "runtime"}})
}

func WithoutTask(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskKey{}, (*taskState)(nil))
}

func TaskSnapshot(ctx context.Context) *task.State {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return task.Clone(&state.state)
}

func UpdateTask(ctx context.Context, update task.Update) error {
	status, summary, remaining := update.Status, update.Summary, update.Remaining
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return fmt.Errorf("no active task")
	}
	if len(update.ExpectedOutcome) > 2048 {
		return fmt.Errorf("expected outcome exceeds 2048 bytes")
	}
	if len(summary) > 2048 {
		return fmt.Errorf("task summary exceeds 2048 bytes")
	}
	if remaining != nil {
		if len(*remaining) > 32 {
			return fmt.Errorf("remaining work accepts up to 32 items")
		}
		for _, item := range *remaining {
			if strings.TrimSpace(item) == "" || len(item) > 512 {
				return fmt.Errorf("remaining work items must contain 1-512 bytes")
			}
		}
	}
	switch status {
	case task.Running, task.Completed, task.Blocked, task.Unfinished, task.Unverified:
	default:
		return fmt.Errorf("unsupported task status %q", status)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	updated := task.Clone(&state.state)
	updated.Status, updated.Source, updated.Summary = status, "model", strings.TrimSpace(summary)
	if update.ExpectedOutcome != "" {
		updated.ExpectedOutcome = strings.TrimSpace(update.ExpectedOutcome)
	}
	if remaining != nil {
		updated.Remaining = append([]string(nil), (*remaining)...)
	}
	if status == task.Completed && len(updated.Pending()) > 0 {
		return fmt.Errorf("task still has pending work; resolve the plan or report blocked, unfinished, or unverified")
	}
	if status != task.Running && updated.Summary == "" {
		return fmt.Errorf("a terminal task assessment requires a summary of results or blockers")
	}
	state.state = *updated
	return nil
}

// ObserveTool records bounded executor facts. It never infers build/test success
// from command names, or promotes a successful write to task verification.
func ObserveTool(ctx context.Context, call message.ToolCall, observed *evidence.Record, failed, readOnly bool, plan []tool.TodoItem) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil || call.Function.Name == "task_state" {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !readOnly || failed || observed.HasErrors() {
		state.state.Status, state.state.Source, state.state.Summary = task.Running, "runtime", ""
	}
	if call.Function.Name == "todo" && !failed {
		state.state.Plan = nil
		for _, item := range plan {
			state.state.Plan = append(state.state.Plan, task.Item{ID: item.ID, Description: item.Task, Status: item.Status})
		}
	}
	entry := task.Observation{CallID: call.ID, Tool: call.Function.Name, Failed: failed, Evidence: evidence.Clone(observed)}
	if entry.Evidence != nil {
		entry.Evidence.Target = boundedTaskText(entry.Evidence.Target, 512)
	}
	state.state.Observations = append(state.state.Observations, entry)
	if len(state.state.Observations) > 24 {
		state.state.Observations = append([]task.Observation(nil), state.state.Observations[len(state.state.Observations)-24:]...)
	}
}

// AssessCompletion allows one reminder for known remaining work. A final reply
// without an explicit assessment is unverified, rather than automatically done.
func AssessCompletion(ctx context.Context, msg *message.Message) (reminder string) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	pending := state.state.Pending()
	if state.state.Status == task.Running || (state.state.Status == task.Completed && len(pending) > 0) {
		if len(pending) > 0 && !state.reminded {
			state.reminded = true
			reminder = "Known task work remains.\n" + boundedTaskText(TaskBrief(&state.state), 3072) + "\nContinue the requested work, or report remaining work and blockers in your final reply. If task_state is available, declare blocked, unfinished, or unverified with a reason. Respect user restrictions on checks; do not run additional tests merely to satisfy this reminder."
		} else {
			state.state.Status, state.state.Source = task.Unverified, "runtime"
			state.state.Summary = "The model returned a final reply without an explicit task assessment."
			if len(pending) > 0 {
				state.state.Status, state.state.Summary = task.Unfinished, "Known requested work remains in the plan."
			}
		}
	}
	msg.TaskState = task.Clone(&state.state)
	return reminder
}

func EndTask(ctx context.Context, status Status) *task.State {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if status != Completed {
		state.state.Status, state.state.Source = string(status), "runtime"
		state.state.Summary = "Execution stopped before a final task assessment."
	}
	return task.Clone(&state.state)
}

func TaskBrief(state *task.State) string {
	if state == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Task outcome: %s (%s assessment). Tool observations are execution facts, not proof that the whole task is correct.\n", state.Status, state.Source)
	if state.ExpectedOutcome != "" {
		fmt.Fprintf(&b, "Expected outcome (model interpretation): %s\n", boundedTaskText(state.ExpectedOutcome, 512))
	}
	if state.Summary != "" {
		fmt.Fprintf(&b, "Assessment: %s\n", boundedTaskText(state.Summary, 512))
	}
	for i, item := range state.Pending() {
		if i == 8 {
			b.WriteString("More work remains; inspect task_state for the full list.\n")
			break
		}
		fmt.Fprintf(&b, "Remaining: %s\n", boundedTaskText(item, 256))
	}
	start := max(0, len(state.Observations)-4)
	for _, entry := range state.Observations[start:] {
		if entry.Evidence == nil {
			continue
		}
		r := entry.Evidence
		fmt.Fprintf(&b, "Observed %s [%s]: %s", entry.Tool, entry.CallID, r.Outcome)
		if r.Target != "" {
			fmt.Fprintf(&b, " target=%s", boundedTaskText(r.Target, 160))
		}
		if r.Changed != nil {
			fmt.Fprintf(&b, " changed=%t", *r.Changed)
		}
		if r.ExitCode != nil {
			fmt.Fprintf(&b, " exit_code=%d", *r.ExitCode)
		}
		if r.HasErrors() || entry.Failed {
			b.WriteString("; error reported")
		}
		b.WriteByte('\n')
		for i, d := range r.Diagnostics {
			if i == 2 {
				break
			}
			fmt.Fprintf(&b, "Diagnostic line %d: %s\n", d.Line, boundedTaskText(d.Message, 180))
		}
	}
	return boundedTaskText(b.String(), 4096)
}

func boundedTaskText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
