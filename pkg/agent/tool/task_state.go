package tool

import (
	"encoding/json"
	"fmt"

	"loop/pkg/agent/run"
	"loop/pkg/domain/task"
)

type taskStateTool struct{}

func NewTaskStateTool() ToolExecutor  { return &taskStateTool{} }
func (t *taskStateTool) Name() string { return "task_state" }
func (t *taskStateTool) PromptSnippet() string {
	return "task_state: Inspect task evidence or declare an outcome and remaining work."
}
func (t *taskStateTool) PromptGuidelines() []string {
	return []string{"Before the final reply on coding work, report completed, blocked, unfinished, or unverified with task_state. Resolve known pending work before declaring completed. A successful command or edit alone does not verify the task. Follow the user's restrictions on validation; never add tests merely to obtain a completed status."}
}
func (t *taskStateTool) Definition() Tool {
	return NewFunctionTool(t.Name(), "Inspect task state and observed tool evidence, or update the model's task assessment. This does not execute checks or prove correctness. Keep remaining work explicit. Completed is rejected while the active plan or remaining list has pending items.", map[string]SchemaProp{
		"action":           {Type: "string", Enum: []string{"get", "update"}},
		"status":           {Type: "string", Enum: []string{"running", "completed", "blocked", "unfinished", "unverified"}},
		"expected_outcome": StringProp("Concise interpretation of the requested result; the original user objective is retained separately."),
		"summary":          StringProp("Results, limitations, or reason work is blocked; required for a final assessment."),
		"remaining":        {Type: "array", Description: "Replace remaining work with up to 32 concise items; use [] only when none remains. The todo plan is tracked separately.", Items: &SchemaProp{Type: "string"}},
	}, "action")
}
func (t *taskStateTool) Execute(ctx AgentContext, arguments string) (string, error) {
	var args struct {
		Action string `json:"action"`
		task.Update
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", err
	}
	if err := ctx.Context().Err(); err != nil {
		return "", err
	}
	switch args.Action {
	case "get":
	case "update":
		if err := run.UpdateTask(ctx.Context(), args.Update); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("action must be get or update")
	}
	state := run.TaskSnapshot(ctx.Context())
	if state == nil {
		return "", fmt.Errorf("no active task")
	}
	data, err := json.Marshal(state)
	return string(data), err
}
