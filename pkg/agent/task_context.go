package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/task"
)

func delegationUserInstructions(messages []db.Message) []string {
	var instructions []string
	start := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if len(messages[i].InheritedInstructions) > 0 {
			instructions = append(instructions, messages[i].InheritedInstructions...)
			start = i
			break
		}
	}
	for _, msg := range messages[start:] {
		if msg.Role != "user" || (msg.ContextKind != "" && !isPinnedInstruction(msg)) {
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
	if current := run.TaskSnapshot(ctx); task.HasWork(current) {
		return current
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if task.HasWork(messages[i].TaskState) {
			return task.Clone(messages[i].TaskState)
		}
	}
	return nil
}

func taskContextMessages(ctx context.Context, messages []db.Message) []db.Message {
	current := run.TaskSnapshot(ctx)
	if !task.NeedsProgress(current) {
		return messages
	}
	brief := run.TaskBrief(current)
	if brief == "" {
		return messages
	}
	brief = "Request progress (assessment, not proof):\n" + brief
	return append(append([]db.Message(nil), messages...), db.Message{Role: "user", ContextKind: "task_context", Content: brief})
}

func isPinnedRequestContext(msg db.Message) bool {
	return isPinnedInstruction(msg) || (msg.Role == "user" && msg.ContextKind == "task_context")
}

func (e *TurnEngine) observeTaskTool(tc db.ToolCall, observed *evidence.Record, failed bool) *task.State {
	execCtx := executionContext(e, e.agent)
	ctx := execCtx.Context()
	plan := run.Plan(ctx, e.agent.GetTodos)
	readOnly := e.agent.Registry.Policy(tc.Function.Name, tc.Function.Arguments).ReadOnly
	run.ObserveTool(ctx, tc, observed, failed, readOnly, plan)
	return run.TaskSnapshot(ctx)
}
