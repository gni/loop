package tool

import (
	"errors"
	"fmt"
	"strings"
)

func scalarToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case float64:
		return fmt.Sprintf("%.0f", val)
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func parseTaskID(arguments string) (string, error) {
	var raw string
	var args struct {
		TaskID    interface{} `json:"task_id"`
		TaskIDAlt interface{} `json:"taskId"`
		ID        interface{} `json:"id"`
		Task      interface{} `json:"task"`
	}
	if err := ParseToolArguments(arguments, &args, func(s string) { raw = s }); err != nil {
		return "", err
	}
	id := FirstNonEmpty(
		strings.TrimSpace(raw),
		scalarToString(args.TaskID),
		scalarToString(args.TaskIDAlt),
		scalarToString(args.ID),
		scalarToString(args.Task),
	)
	if id == "" {
		return "", fmt.Errorf("missing required argument: task_id")
	}
	return id, nil
}

func resolveTaskIDArg(ctx AgentContext, arguments, missingKey, missingFallback string) (string, error) {
	taskID, err := parseTaskID(arguments)
	if err != nil {
		return "", errors.New(RuntimeMessage(missingKey, missingFallback))
	}
	if r, ok := ctx.(interface{ ResolveTaskID(string) string }); ok {
		taskID = r.ResolveTaskID(taskID)
	}
	return taskID, nil
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
			"task_id": StringProp(FormatParamDescription("task_status", "task_id", "The ID of the background task (e.g. 'task_1').")),
		},
		"task_id",
	)
}

func (t *taskStatusTool) Execute(ctx AgentContext, arguments string) (string, error) {
	taskID, err := resolveTaskIDArg(ctx, arguments, "task_missing_arg_status", "missing required argument 'task_id'. Provide the ID of the background task (e.g. 'task_1').")
	if err != nil {
		return "", err
	}

	status, output, err := ctx.GetTaskStatus(taskID)
	if err != nil {
		return "", errors.New(RuntimeMessagef("task_status_not_found", "task %q not found. The task may have already completed, was killed, or does not exist.", taskID))
	}

	var cmdStr string
	if tc, ok := ctx.(interface{ GetTaskCommand(string) string }); ok {
		cmdStr = tc.GetTaskCommand(taskID)
	}
	if cmdStr != "" {
		displayCmd := strings.TrimSpace(cmdStr)
		if idx := strings.Index(displayCmd, "\n"); idx != -1 {
			displayCmd = strings.TrimSpace(displayCmd[:idx])
		}
		if len(displayCmd) > 50 {
			displayCmd = displayCmd[:47] + "..."
		}
		return RuntimeMessagef("task_status_cmd_output", "Task %s (%s) is currently: %s\n\nOutput:\n%s", taskID, displayCmd, status, output), nil
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
			"task_id": StringProp(FormatParamDescription("task_kill", "task_id", "The ID of the task to terminate.")),
		},
		"task_id",
	)
}

func (t *taskKillTool) Execute(ctx AgentContext, arguments string) (string, error) {
	taskID, err := resolveTaskIDArg(ctx, arguments, "task_missing_arg_kill", "missing required argument 'task_id'. Provide the ID of the background task to kill (e.g. 'task_1').")
	if err != nil {
		return "", err
	}

	if err := ctx.KillTask(taskID); err != nil {
		return "", fmt.Errorf(RuntimeMessage("task_kill_failed", "failed to terminate task %q: %w"), taskID, err)
	}

	return RuntimeMessagef("task_kill_success", "Task %s successfully terminated.", taskID), nil
}