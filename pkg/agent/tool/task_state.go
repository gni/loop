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
	return "task_state: Inspect the coding objective or report its outcome and remaining work."
}
func (t *taskStateTool) PromptGuidelines() []string {
	return FormatToolGuidelines(t.Name(), []string{"Optional after tool work starts: use for a changed outcome or blocker. Skip routine updates and calls made only to precede a final reply. Resolve pending work before declaring completed and follow the user's validation restrictions."})
}
func (t *taskStateTool) Definition() Tool {
	return NewFunctionTool(t.Name(), FormatToolDescription(t.Name(), "Read request progress or update its outcome. Separate from background commands (get_task_status). Does not execute checks. Completion requires no pending work. Returns a compact report; evidence stays internal."), map[string]SchemaProp{
		"action":            {Type: "string", Enum: []string{"get", "update"}},
		"status":            {Type: "string", Enum: []string{"running", "completed", "blocked", "unfinished", "unverified"}},
		"expected_outcome":  StringProp("Concise interpretation of the requested result; the original user objective is retained separately."),
		"summary":           StringProp("Results, limitations, or reason work is blocked; required for a final assessment."),
		"verification_note": StringProp("Explain skipped, forbidden or unavailable verification. Does not establish check success."),
		"remaining":         {Type: "array", Description: "Replace remaining work with up to 32 concise items; use [] only when none remains. The todo plan is tracked separately.", Items: &SchemaProp{Type: "string"}},
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
	var state *task.State
	changed := true
	switch args.Action {
	case "get":
		state = run.TaskSnapshot(ctx.Context())
	case "update":
		var err error
		state, changed, err = run.UpdateTaskSnapshot(ctx.Context(), args.Update)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("action must be get or update")
	}
	if state == nil {
		return "", fmt.Errorf("no active task")
	}
	if args.Action == "update" && !changed {
		return `{"unchanged":true}`, nil
	}
	data, err := json.Marshal(task.ProgressReport(state, args.Action == "get"))
	return string(data), err
}
