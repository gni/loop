package run

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"slices"
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
	mu               sync.Mutex
	state            task.State
	reminded         bool
	processMu        sync.Mutex // Serializes output snapshots and their cursors.
	processOutput    map[string]processOutputCursor
	visibleTools     map[string]bool
	discoveredTools  map[string]bool
	toolAllowlist    []string
	toolAllowlistSet bool
	progress         uint64
	seenEvidence     map[string]bool
	planTracked      bool
}

func NoteDiscoveredTool(ctx context.Context, name string) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.discoveredTools == nil {
		state.discoveredTools = make(map[string]bool)
	}
	state.discoveredTools[tool.NormalizeName(name)] = true
}

func ToolDiscovered(ctx context.Context, name string) bool {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.discoveredTools[tool.NormalizeName(name)]
}

func NoteToolAllowlist(ctx context.Context, allowlist []string) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.toolAllowlist = slices.Clone(allowlist)
	state.toolAllowlistSet = true
}

func ToolSearchAllowlist(ctx context.Context, fallback []string) []string {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return fallback
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.toolAllowlistSet {
		return slices.Clone(state.toolAllowlist)
	}
	return fallback
}

func NoteVisibleTools(ctx context.Context, names []string) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.visibleTools = make(map[string]bool, len(names))
	for _, name := range names {
		state.visibleTools[tool.NormalizeName(name)] = true
	}
}

func ToolVisible(ctx context.Context, name string) bool {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.visibleTools == nil || state.visibleTools[tool.NormalizeName(name)]
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
	syncTaskPlan(ctx)
	constraints := Instructions(ctx)
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(constraints) > 0 {
		state.state.Constraints = latestInstructions(append(state.state.Constraints, constraints...))
	}
	return task.Clone(&state.state)
}

func SuggestVerification(ctx context.Context, checks []evidence.Check) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, check := range checks {
		found := false
		for _, current := range state.state.Verification.Checks {
			if current.Kind == check.Kind {
				found = true
				break
			}
		}
		if !found {
			state.state.Verification.Checks = append(state.state.Verification.Checks, check)
		}
	}
}

func UpdateTask(ctx context.Context, update task.Update) error {
	_, _, err := UpdateTaskSnapshot(ctx, update)
	return err
}

// UpdateTaskSnapshot commits an assessment and detects repeats under one lock.
func UpdateTaskSnapshot(ctx context.Context, update task.Update) (*task.State, bool, error) {
	syncTaskPlan(ctx)
	status, summary, remaining := update.Status, update.Summary, update.Remaining
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return nil, false, fmt.Errorf("no active task")
	}
	if len(update.ExpectedOutcome) > 2048 {
		return nil, false, fmt.Errorf("expected outcome exceeds 2048 bytes")
	}
	if len(summary) > 2048 {
		return nil, false, fmt.Errorf("task summary exceeds 2048 bytes")
	}
	if len(update.VerificationNote) > 512 {
		return nil, false, fmt.Errorf("verification note exceeds 512 bytes")
	}
	if remaining != nil {
		if len(*remaining) > 32 {
			return nil, false, fmt.Errorf("remaining work accepts up to 32 items")
		}
		for _, item := range *remaining {
			if strings.TrimSpace(item) == "" || len(item) > 512 {
				return nil, false, fmt.Errorf("remaining work items must contain 1-512 bytes")
			}
		}
	}
	switch status {
	case task.Running, task.Completed, task.Blocked, task.Unfinished, task.Unverified:
	default:
		return nil, false, fmt.Errorf("unsupported task status %q", status)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	updated := task.Clone(&state.state)
	updated.Status, updated.Source, updated.Summary = status, "model", strings.TrimSpace(summary)
	if update.VerificationNote != "" {
		updated.Verification.Note = strings.TrimSpace(update.VerificationNote)
	}
	if update.ExpectedOutcome != "" {
		updated.ExpectedOutcome = strings.TrimSpace(update.ExpectedOutcome)
	}
	if remaining != nil {
		updated.Remaining = append([]string(nil), (*remaining)...)
	}
	if status == task.Completed && len(updated.Pending()) > 0 {
		return nil, false, fmt.Errorf("task still has pending work; resolve the plan or report blocked, unfinished, or unverified")
	}
	if status != task.Running && updated.Summary == "" {
		return nil, false, fmt.Errorf("a terminal task assessment requires a summary of results or blockers")
	}
	changed := !reflect.DeepEqual(&state.state, updated)
	state.state = *updated
	return task.Clone(updated), changed, nil
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
		state.planTracked = true
		state.state.Plan = nil
		for _, item := range plan {
			state.state.Plan = append(state.state.Plan, task.Item{ID: item.ID, Description: item.Task, Status: item.Status})
		}
	}
	entry := task.Observation{CallID: call.ID, Tool: call.Function.Name, Failed: failed, Evidence: evidence.Clone(observed)}
	if observed != nil && observed.Outcome == evidence.Succeeded && observed.Revision != "" {
		if previous := state.state.Revisions[observed.Target]; previous != "" && previous != observed.Revision {
			state.state.Verification.Invalidate(observed.Target)
			state.state.Revisions[observed.Target] = observed.Revision
		}
	}
	state.state.Verification.Observe(observed)
	if entry.Evidence != nil {
		entry.Evidence.Target = boundedTaskText(entry.Evidence.Target, 512)
	}
	observeLedger(state, entry)
	state.state.Observations = append(state.state.Observations, entry)
	if len(state.state.Observations) > 24 {
		state.state.Observations = append([]task.Observation(nil), state.state.Observations[len(state.state.Observations)-24:]...)
	}
}

// AssessCompletion allows one reminder for known remaining work. A final reply
// without an explicit assessment is unverified, rather than automatically done.
func AssessCompletion(ctx context.Context, msg *message.Message) string {
	return assessCompletion(ctx, msg, true)
}

func assessCompletion(ctx context.Context, msg *message.Message, allowReminder bool) (reminder string) {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !task.HasWork(&state.state) {
		return ""
	}
	pending := state.state.Pending()
	if state.state.Status == task.Running || (state.state.Status == task.Completed && len(pending) > 0) {
		if allowReminder && len(pending) > 0 && !state.reminded {
			state.reminded = true
			reminder = "Known task work remains.\n" + boundedTaskText(TaskBrief(&state.state), 3072) + "\nFinish the requested work or explain what remains and why. Follow the user's validation restrictions."
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
	if status == Completed && !task.HasWork(&state.state) {
		return nil
	}
	if status != Completed {
		preserveReason := state.state.Status == string(status) && state.state.Summary != ""
		state.state.Status, state.state.Source = string(status), "runtime"
		if !preserveReason {
			state.state.Summary = "Execution stopped before a final task assessment."
		}
	}
	return task.Clone(&state.state)
}

func TaskBrief(state *task.State) string {
	if state == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Request status: %s (%s assessment).\n", state.Status, state.Source)
	if state.Verification.Stage != "" {
		fmt.Fprintf(&b, "Execution evidence: %s. Build/lint/test success requires an observed command exit; completion is a separate assessment.\n", state.Verification.Stage)
		shown := 0
		for _, check := range state.Verification.Checks {
			if check.Status == evidence.Passed || check.Status == "" {
				continue
			}
			if shown == 4 {
				b.WriteString("Additional check results retained in task metadata.\n")
				break
			}
			shown++
			fmt.Fprintf(&b, "Check %s (%s): %s\n", check.Kind, check.Status, boundedTaskText(check.Command, 192))
			if check.Detail != "" {
				fmt.Fprintf(&b, "Check detail: %s\n", boundedTaskText(check.Detail, 192))
			}
		}
		b.WriteString("Use only relevant checks permitted by the user; report skipped checks and reasons.\n")
		if state.Verification.Note != "" {
			fmt.Fprintf(&b, "Verification limitation: %s\n", state.Verification.Note)
		}
	}
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

	b.WriteString(boundedTaskText(ledgerBrief(state), 768))
	return boundedTaskText(b.String(), 3072)
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

type processOutputCursor struct {
	size      int
	hash      [32]byte
	version   int64
	stdout    int64
	stderr    int64
	startedAt int64
	streams   bool
}

// NewBackgroundOutput avoids resending cumulative stdout on every poll.
// The full buffer remains owned by the process and available in /task view.
func NewBackgroundOutput(ctx context.Context, id, output string, versions ...int64) string {
	state, _ := ctx.Value(taskKey{}).(*taskState)
	if state == nil {
		return output
	}
	state.processMu.Lock()
	defer state.processMu.Unlock()
	if !utf8.ValidString(output) {
		delete(state.processOutput, id)
		return output
	}
	version := int64(0)
	if len(versions) > 0 {
		version = versions[0]
	}
	previous, exists := state.processOutput[id]
	exists = exists && !previous.streams
	next := processOutputCursor{size: len(output), hash: sha256.Sum256([]byte(output)), version: version}
	state.rememberProcessOutput(id, next)
	// Identical text can still be new data after a rolling buffer fills.
	if exists && version != previous.version && next.size == previous.size && next.hash == previous.hash {
		return output
	}
	if exists && len(output) >= previous.size && sha256.Sum256([]byte(output[:previous.size])) == previous.hash {
		if len(output) == previous.size {
			return "(no new output)"
		}
		return "[New output since previous status]\n" + output[previous.size:]
	}
	return output
}
