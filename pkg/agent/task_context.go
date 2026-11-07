package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/run"
	filetool "loop/pkg/agent/tool/file"
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

func taskSnapshotForHistory(ctx context.Context, messages []db.Message) *task.State {
	if current := run.TaskSnapshot(ctx); current != nil {
		return current
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].TaskState != nil {
			return task.Clone(messages[i].TaskState)
		}
	}
	return nil
}

func taskContextMessages(ctx context.Context, messages []db.Message) []db.Message {
	current := run.TaskSnapshot(ctx)
	brief := run.TaskBrief(current)
	if brief == "" {
		return messages
	}
	brief = "Runtime task state (completion assessments are not independent verification):\n" + brief
	brief += "\nFor coding work, if task_state is available, use it to report the final outcome. Respect the user's validation restrictions. Inherited parent instructions constrain a delegated task; perform only the delegated scope."
	for i := len(messages) - 1; i >= 0; i-- {
		prior := messages[i].TaskState
		if prior == nil || prior.ID == current.ID {
			continue
		}
		if prior.Status != task.Completed {
			brief += "\nPrevious task (context only; the current request defines scope): " + boundedContextText(prior.Objective, 384) + "\n" + boundedContextText(run.TaskBrief(prior), 1536)
		}
		break
	}
	return append(append([]db.Message(nil), messages...), db.Message{Role: "user", ContextKind: "task_context", Content: brief})
}

func boundedContextText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return filetool.UTF8SafePrefix(s, n) + "…"
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
