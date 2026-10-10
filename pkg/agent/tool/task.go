package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func parseTaskID(arguments string) (string, error) {
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			trimmed = unquoted
		}
	} else if strings.HasPrefix(trimmed, "{") {
		var args struct {
			TaskID    string `json:"task_id"`
			TaskIDAlt string `json:"taskId"`
			ID        string `json:"id"`
			Task      string `json:"task"`
		}
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		if v := FirstNonEmpty(args.TaskID, args.TaskIDAlt, args.ID, args.Task); v != "" {
			trimmed = v
		}
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", fmt.Errorf("missing required argument: task_id")
	}
	return trimmed, nil
}

type taskStatusTool struct{}

func NewTaskStatusTool() ToolExecutor {
	return &taskStatusTool{}
}

func (t *taskStatusTool) Name() string { return "task_status" }

func (t *taskStatusTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Retrieve the execution status and output of a background task")
}

func (t *taskStatusTool) Definition() Tool {
	return NewFunctionTool(
		"task_status",
		FormatToolDescription("task_status", "Retrieve the execution status and buffered stdout/stderr output of a background task."),
		map[string]SchemaProp{
			"task_id": {
				Type:        "string",
				Description: FormatParamDescription("task_status", "task_id", "The ID of the background task (e.g. 'task_1')."),
			},
		},
		"task_id",
	)
}

func (t *taskStatusTool) Execute(ctx AgentContext, arguments string) (string, error) {
	taskID, err := parseTaskID(arguments)
	if err != nil {
		return "", errors.New(RuntimeMessage("task_missing_arg_status", "missing required argument 'task_id'. Provide the ID of the background task (e.g. 'task_1')."))
	}

	status, output, err := ctx.GetTaskStatus(taskID)
	if err != nil {
		return "", errors.New(RuntimeMessagef("task_status_not_found", "task %q not found. The task may have already completed, was killed, or does not exist.", taskID))
	}

	return RuntimeMessagef("task_status_output", "Task %s is currently: %s\n\nOutput:\n%s", taskID, status, output), nil
}

type taskKillTool struct{}

func NewTaskKillTool() ToolExecutor {
	return &taskKillTool{}
}

func (t *taskKillTool) Name() string { return "task_kill" }

func (t *taskKillTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Terminate a running background task")
}

func (t *taskKillTool) Definition() Tool {
	return NewFunctionTool(
		"task_kill",
		FormatToolDescription("task_kill", "Terminate a running background task."),
		map[string]SchemaProp{
			"task_id": {
				Type:        "string",
				Description: FormatParamDescription("task_kill", "task_id", "The ID of the task to terminate."),
			},
		},
		"task_id",
	)
}

func (t *taskKillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	taskID, err := parseTaskID(arguments)
	if err != nil {
		return "", errors.New(RuntimeMessage("task_missing_arg_kill", "missing required argument 'task_id'. Provide the ID of the background task to kill (e.g. 'task_1')."))
	}

	err = ctx.KillTask(taskID)
	if err != nil {
		return "", fmt.Errorf(RuntimeMessage("task_kill_failed", "failed to terminate task %q: %w"), taskID, err)
	}

	return RuntimeMessagef("task_kill_success", "Task %s successfully terminated.", taskID), nil
}
