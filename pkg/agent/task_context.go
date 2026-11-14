package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/task"
	"loop/pkg/resources"
)

func delegationUserInstructions(messages []db.Message) []string {
	var instructions []string
	start := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if len(messages[i].InheritedInstructions) > 0 {
			for _, instruction := range messages[i].InheritedInstructions {
				if !(db.Message{Role: "user", Content: instruction}).IsShellContext() {
					instructions = append(instructions, instruction)
				}
			}
			start = i
			break
		}
	}
	for _, msg := range messages[start:] {
		if msg.Role != "user" || msg.IsShellContext() || (msg.ContextKind != "" && !isPinnedInstruction(msg)) {
			continue
		}
		if strings.HasPrefix(msg.Content, "[user manually executed slash command:") {
			continue
		}
		if value := strings.TrimSpace(msg.Content); value != "" {
			instructions = append(instructions, value)
		}
	}
	return instructions
}

// Keep meaningful work in the saved summary even when compaction happens
// during casual chat. Historical metadata does not activate the current task.
func taskSnapshotForHistory(ctx context.Context, messages []db.Message) *task.State {
	if current := run.TaskSnapshot(ctx); task.HasWork(current) || task.CanResume(current) {
		return current
	}
	return task.Clone(latestHistoricalTask(messages))
}

// Select once: a finished or interrupted task must not fall through to an
// obsolete snapshot from earlier in the same conversation.
func latestHistoricalTask(messages []db.Message) *task.State {
	for i := len(messages) - 1; i >= 0; i-- {
		if state := messages[i].TaskState; state != nil && (state.Status == task.Completed || task.CanResume(state)) {
			return state
		}
	}
	return nil
}

func taskContextMessages(ctx context.Context, messages []db.Message) []db.Message {
	current := run.TaskSnapshot(ctx)
	if !task.HasWork(current) {
		if run.RestoreTask(ctx, latestHistoricalTask(messages)) {
			current = run.TaskSnapshot(ctx)
		}
	}
	if !task.NeedsProgress(current) {
		return messages
	}
	if current.Status == task.Running && current.ExpectedOutcome == "" && current.Summary == "" && len(current.Pending()) == 0 {
		return messages
	}
	brief := run.TaskBrief(current)
	if brief == "" {
		return messages
	}
	brief = "Request progress (assessment, not proof):\n" + brief
	output := make([]db.Message, 0, len(messages)+1)
	for _, msg := range messages {
		if msg.ContextKind != "task_context" {
			output = append(output, msg)
		}
	}
	return append(output, db.Message{Role: "user", ContextKind: "task_context", Content: brief})
}

func isPinnedRequestContext(msg db.Message) bool {
	return isPinnedInstruction(msg) || (msg.Role == "user" && (msg.ContextKind == "task_context" || msg.ContextKind == "skill" || msg.ContextKind == "system"))
}

func (e *TurnEngine) observeTaskTool(tc db.ToolCall, observed *evidence.Record, failed bool) *task.State {
	execCtx := executionContext(e, e.agent)
	ctx := execCtx.Context()
	plan := run.Plan(ctx, e.agent.GetTodos)
	readOnly := e.agent.Registry.Policy(tc.Function.Name, tc.Function.Arguments).ReadOnly
	run.ObserveTool(ctx, tc, observed, failed, readOnly, plan)
	if observed != nil && observed.Changed != nil && *observed.Changed {
		run.SuggestVerification(ctx, resources.VerificationChecks(e.agent.GetWorkspaceRoot()))
	}
	return run.TaskSnapshot(ctx)
}
