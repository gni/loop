package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"loop/pkg/agent/run"
	"loop/pkg/agent/tool/file"
	domaintool "loop/pkg/domain/tool"
)

var errTaskIDMissing = errors.New("missing required argument: task_id")

func taskIDString(v any) (string, error) {
	switch value := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(value), nil
	case float64:
		number, err := domaintool.IntegerArgument("task_id", value)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(number), nil
	case int:
		if value >= 0 {
			return strconv.Itoa(value), nil
		}
	case int64:
		if value >= 0 {
			return strconv.FormatInt(value, 10), nil
		}
	}
	return "", fmt.Errorf("task_id must be a string or a nonnegative integer")
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
	var id string
	for _, value := range []any{raw, args.TaskID, args.TaskIDAlt, args.ID, args.Task} {
		candidate, err := taskIDString(value)
		if err != nil {
			return "", err
		}
		if candidate == "" {
			continue
		}
		if id != "" && candidate != id {
			return "", fmt.Errorf("conflicting task IDs")
		}
		id = candidate
	}
	if id == "" {
		return "", errTaskIDMissing
	}
	return id, nil
}

func resolveTaskIDArg(ctx AgentContext, arguments, missingKey, missingFallback string) (string, error) {
	taskID, err := parseTaskID(arguments)
	if err != nil {
		if errors.Is(err, errTaskIDMissing) {
			return "", errors.New(RuntimeMessage(missingKey, missingFallback))
		}
		return "", err
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

func (t *taskStatusTool) Name() string { return "get_task_status" }

func (t *taskStatusTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Poll background command status and new output; full=true rereads its buffer")
}

func (t *taskStatusTool) Definition() Tool {
	return NewFunctionTool(
		"get_task_status",
		FormatToolDescription("get_task_status", "Retrieve background command status and new stdout/stderr since the previous poll. Use full=true to reread the complete buffer."),
		map[string]SchemaProp{
			"task_id": StringProp(FormatParamDescription("get_task_status", "task_id", "The ID of the background task (e.g. 'task_1').")),
			"full":    {Type: "boolean", Description: "Return the full buffered output instead of only new output."},
		},
		"task_id",
	)
}

func (t *taskStatusTool) Execute(ctx AgentContext, arguments string) (string, error) {
	taskID, err := resolveTaskIDArg(ctx, arguments, "task_missing_arg_status", "missing required argument 'task_id'. Provide the ID of the background task (e.g. 'task_1').")
	if err != nil {
		return "", err
	}

	var options struct {
		Full bool `json:"full"`
	}
	_ = json.Unmarshal([]byte(arguments), &options)
	readStatus := ctx.GetTaskStatus
	if options.Full {
		if full, ok := ctx.(domaintool.FullTaskStatusProvider); ok {
			readStatus = full.GetFullTaskStatus
		}
	}
	status, output, err := readStatus(taskID)
	if err != nil {
		return "", errors.New(RuntimeMessagef("task_status_not_found", "task %q not found. The task may have already completed, was killed, or does not exist.", taskID))
	}

	fullOutput := output
	if versioned, ok := ctx.(domaintool.TaskOutputVersionProvider); ok {
		output = run.NewBackgroundOutput(ctx.Context(), taskID, output, versioned.GetTaskOutputVersion(taskID))
	} else {
		output = run.NewBackgroundOutput(ctx.Context(), taskID, output)
	}

	if options.Full {
		output = fullOutput
	}

	var cmdStr string
	if tc, ok := ctx.(domaintool.TaskCommandProvider); ok {
		cmdStr = tc.GetTaskCommand(taskID)
	}
	if cmdStr != "" {
		displayCmd := strings.TrimSpace(cmdStr)
		if idx := strings.Index(displayCmd, "\n"); idx != -1 {
			displayCmd = strings.TrimSpace(displayCmd[:idx])
		}
		if len(displayCmd) > 50 {
			displayCmd = file.UTF8SafePrefix(displayCmd, 47) + "..."
		}
		return RuntimeMessagef("task_status_cmd_output", "Task %s (%s) is currently: %s\n\nOutput:\n%s", taskID, displayCmd, status, output), nil
	}

	return RuntimeMessagef("task_status_output", "Task %s is currently: %s\n\nOutput:\n%s", taskID, status, output), nil
}

type taskKillTool struct{}

func NewTaskKillTool() ToolExecutor {
	return &taskKillTool{}
}

func (t *taskKillTool) Name() string { return "stop_task" }

func (t *taskKillTool) PromptSnippet() string {
	return FormatToolSnippet(t.Name(), "Terminate a running background task")
}

func (t *taskKillTool) Definition() Tool {
	return NewFunctionTool(
		"stop_task",
		FormatToolDescription("stop_task", "Terminate a running background task."),
		map[string]SchemaProp{
			"task_id": StringProp(FormatParamDescription("stop_task", "task_id", "The ID of the task to terminate.")),
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
