package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	domaintool "loop/pkg/domain/tool"
)

type todoTool struct {
	mu    sync.RWMutex
	items []domaintool.TodoItem
}

func NewTodoTool() domaintool.ToolExecutor {
	return &todoTool{}
}

func (t *todoTool) Name() string { return "todo" }

func (t *todoTool) Definition() domaintool.Tool {
	return domaintool.Tool{
		Type: "function",
		Function: domaintool.FunctionDefinition{
			Name:        "todo",
			Description: domaintool.FormatToolDescription("todo", "Manage and track progress on multi-step tasks. Call this tool to initialize, update, or track your implementation plan. Send the entire updated list on every call, or specify action ('update', 'add', 'remove') with task_id and status. Exactly one task may be 'in_progress' at a time."),
			Parameters: domaintool.JSONSchema{
				Type: "object",
				Properties: map[string]domaintool.SchemaProp{
					"tasks": {
						Type:        "array",
						Description: domaintool.FormatParamDescription("todo", "tasks", "The complete updated list of tasks representing the active plan."),
						Items: &domaintool.SchemaProp{
							Type: "object",
							Properties: map[string]domaintool.SchemaProp{
								"id":     domaintool.StringProp(domaintool.FormatParamDescription("todo", "id", "Unique identifier for the task (e.g., 'task-1', 'step-1').")),
								"task":   domaintool.StringProp(domaintool.FormatParamDescription("todo", "task", "Clear, concrete description of the task.")),
								"status": {Type: "string", Description: domaintool.FormatParamDescription("todo", "status", "Task status. Exactly one task may be 'in_progress' at any given moment."), Enum: []string{"pending", "in_progress", "completed"}},
							},
							Required: []string{"id", "task", "status"},
						},
					},
					"action":  {Type: "string", Description: domaintool.FormatParamDescription("todo", "action", "Action to perform on the plan: 'update', 'add', 'remove', or 'list'."), Enum: []string{"update", "add", "remove", "list"}},
					"task_id": domaintool.StringProp(domaintool.FormatParamDescription("todo", "task_id", "Identifier of the task to update or remove (e.g., 'task-1').")),
					"status":  {Type: "string", Description: domaintool.FormatParamDescription("todo", "status", "Updated status when modifying a task ('pending', 'in_progress', 'completed')."), Enum: []string{"pending", "in_progress", "completed"}},
					"task":    domaintool.StringProp(domaintool.FormatParamDescription("todo", "task", "Description of the task when adding or modifying.")),
				},
			},
		},
	}
}

func (t *todoTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Manage structured implementation plan. Whole-list replacement or action-based update; exactly one task in_progress.")
}

func (t *todoTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Task Planning: For complex multi-step objectives, use the 'todo' tool to initialize and update your plan. Mark the active task 'in_progress' and check off tasks as 'completed'.",
	})
}

func matchTaskID(targetID, itemID string, index int) bool {
	tgt := strings.ToLower(strings.TrimSpace(targetID))
	itm := strings.ToLower(strings.TrimSpace(itemID))
	if tgt == "" || itm == "" {
		return false
	}
	if tgt == itm || tgt == fmt.Sprintf("%d", index+1) {
		return true
	}
	strip := func(s string) string {
		for _, prefix := range []string{"task-", "task_", "step-", "step_", "#"} {
			if strings.HasPrefix(s, prefix) {
				return strings.TrimPrefix(s, prefix)
			}
		}
		return s
	}
	st, si := strip(tgt), strip(itm)
	return st == si || si == tgt || st == itm
}

func findTaskIndex(tasks []domaintool.TodoItem, targetID string) int {
	for i, it := range tasks {
		if matchTaskID(targetID, it.ID, i) {
			return i
		}
	}
	return -1
}

func firstNonEmptyList(lists ...[]domaintool.TodoItem) []domaintool.TodoItem {
	for _, l := range lists {
		if len(l) > 0 {
			return l
		}
	}
	return nil
}

func extractMapString(raw map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func extractTaskDescription(raw map[string]interface{}) string {
	for _, k := range []string{"task", "content", "description", "title", "name", "text", "desc", "summary", "item", "body", "label", "value"} {
		if s, ok := raw[k].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func extractRawStatus(raw map[string]interface{}) string {
	if s := strings.ToLower(extractMapString(raw, "status", "state")); s != "" {
		return s
	}
	if done, ok := raw["done"].(bool); ok && done {
		return "completed"
	}
	if comp, ok := raw["completed"].(bool); ok && comp {
		return "completed"
	}
	if ip, ok := raw["in_progress"].(bool); ok && ip {
		return "in_progress"
	}
	return ""
}

func demoteOtherInProgress(tasks []domaintool.TodoItem, activeIdx int) {
	for i := range tasks {
		if i != activeIdx && strings.EqualFold(tasks[i].Status, "in_progress") {
			tasks[i].Status = "pending"
		}
	}
}

func normalizeTodoStatus(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "in_progress", "inprogress", "active", "doing", "working":
		return "in_progress", true
	case "completed", "done", "finished", "success":
		return "completed", true
	case "pending", "todo", "waiting", "planned", "":
		return "pending", true
	default:
		return "", false
	}
}

func resolveTodoAction(action, status, taskID, taskDesc string) (string, string) {
	switch action {
	case "complete", "completed", "finish", "done":
		return "update", domaintool.FirstNonEmpty(status, "completed")
	case "start", "in_progress", "inprogress":
		return "update", domaintool.FirstNonEmpty(status, "in_progress")
	case "pending", "reset":
		return "update", domaintool.FirstNonEmpty(status, "pending")
	case "delete", "del", "rm":
		return "remove", status
	case "create", "insert", "append", "new":
		return "add", status
	case "modify", "edit", "change", "set", "mark", "set_status":
		return "update", status
	case "list", "get", "show", "view", "read":
		return "list", status
	case "":
		if taskID != "" && (status != "" || taskDesc != "") {
			return "update", status
		}
	}
	return action, status
}

func parseMarkdownTodoLines(raw string) []domaintool.TodoItem {
	var parsed []domaintool.TodoItem
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		status := "pending"
		switch {
		case strings.HasPrefix(line, "- [x]"), strings.HasPrefix(line, "[x]"), strings.HasPrefix(line, "- [X]"), strings.HasPrefix(line, "[X]"):
			status = "completed"
			line = strings.TrimSpace(strings.TrimLeft(line, "- [xX] "))
		case strings.HasPrefix(line, "- [>]"), strings.HasPrefix(line, "[>]"), strings.HasPrefix(line, "- [~]"), strings.HasPrefix(line, "[~]"), strings.HasPrefix(line, "- [in_progress]"):
			status = "in_progress"
			line = strings.TrimSpace(strings.TrimLeft(line, "- [>~in_progress] "))
		case strings.HasPrefix(line, "- [ ]"), strings.HasPrefix(line, "[ ]"):
			line = strings.TrimSpace(strings.TrimLeft(line, "- [ ] "))
		case strings.HasPrefix(line, "• "), strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		}

		idx := 0
		for idx < len(line) && line[idx] >= '0' && line[idx] <= '9' {
			idx++
		}
		if idx > 0 && idx < len(line) && (line[idx] == '.' || line[idx] == ')') {
			line = strings.TrimSpace(line[idx+1:])
		}
		if line != "" {
			parsed = append(parsed, domaintool.TodoItem{
				ID:     fmt.Sprintf("task-%d", len(parsed)+1),
				Task:   line,
				Status: status,
			})
		}
	}
	return parsed
}

func (t *todoTool) applyActionObject(raw map[string]interface{}, currentTasks []domaintool.TodoItem) (items []domaintool.TodoItem, listOutput string, isList bool, err error) {
	rawAction := strings.ToLower(extractMapString(raw, "action", "op", "operation", "command", "type"))
	taskID := extractMapString(raw, "task_id", "taskId", "id", "number", "index", "key")
	rawStatus := extractRawStatus(raw)
	taskDesc := extractTaskDescription(raw)

	action, status := resolveTodoAction(rawAction, rawStatus, taskID, taskDesc)

	switch action {
	case "list":
		if len(currentTasks) == 0 {
			return nil, "No active todo plan.", true, nil
		}
		return nil, t.formatSummary(currentTasks), true, nil

	case "update":
		if len(currentTasks) == 0 {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_no_plan_to_update", "cannot update task: no active todo plan exists. Please initialize a plan first with 'tasks'"))
		}
		if taskID == "" {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_missing_task_id", "task_id is required to update a task"))
		}
		matchedIdx := findTaskIndex(currentTasks, taskID)
		if matchedIdx == -1 {
			return nil, "", false, fmt.Errorf(domaintool.RuntimeMessage("todo_task_not_found", "task '%s' not found in current plan"), taskID)
		}
		if status != "" {
			currentTasks[matchedIdx].Status = status
		}
		if taskDesc != "" {
			currentTasks[matchedIdx].Task = taskDesc
		}
		if strings.EqualFold(currentTasks[matchedIdx].Status, "in_progress") {
			demoteOtherInProgress(currentTasks, matchedIdx)
		}
		return currentTasks, "", false, nil

	case "remove":
		if len(currentTasks) == 0 {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_no_plan_to_remove", "cannot remove task: no active todo plan exists"))
		}
		if taskID == "" {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_missing_task_id", "task_id is required to remove a task"))
		}
		matchedIdx := findTaskIndex(currentTasks, taskID)
		if matchedIdx == -1 {
			return nil, "", false, fmt.Errorf(domaintool.RuntimeMessage("todo_task_not_found", "task '%s' not found in current plan"), taskID)
		}
		updated := make([]domaintool.TodoItem, 0, len(currentTasks)-1)
		for i, it := range currentTasks {
			if !matchTaskID(taskID, it.ID, i) {
				updated = append(updated, it)
			}
		}
		return updated, "", false, nil

	case "add":
		if taskDesc == "" {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_empty_desc", "task description is required to add a task"))
		}
		taskID = domaintool.FirstNonEmpty(taskID, fmt.Sprintf("task-%d", len(currentTasks)+1))
		status = domaintool.FirstNonEmpty(status, "pending")
		if strings.EqualFold(status, "in_progress") {
			demoteOtherInProgress(currentTasks, -1)
		}
		return append(currentTasks, domaintool.TodoItem{ID: taskID, Task: taskDesc, Status: status}), "", false, nil

	default:
		if taskDesc == "" {
			return nil, "", false, nil
		}
		if len(currentTasks) == 0 {
			return []domaintool.TodoItem{{
				ID:     domaintool.FirstNonEmpty(taskID, "task-1"),
				Task:   taskDesc,
				Status: domaintool.FirstNonEmpty(status, "pending"),
			}}, "", false, nil
		}
		if taskID != "" {
			if i := findTaskIndex(currentTasks, taskID); i != -1 {
				currentTasks[i].Task = taskDesc
				if status != "" {
					currentTasks[i].Status = status
				}
				return currentTasks, "", false, nil
			}
		}
		return nil, "", false, nil
	}
}

func validateTodoItems(items []domaintool.TodoItem) ([]domaintool.TodoItem, error) {
	seenIDs := make(map[string]bool, len(items))
	inProgressCount := 0

	for i, item := range items {
		cleanID := domaintool.FirstNonEmpty(strings.TrimSpace(item.ID), fmt.Sprintf("task-%d", i+1))
		items[i].ID = cleanID
		if seenIDs[cleanID] {
			return nil, errors.New(domaintool.RuntimeMessagef("todo_duplicate_id", "duplicate task id: '%s' appears more than once", cleanID))
		}
		seenIDs[cleanID] = true

		taskDesc := strings.TrimSpace(item.Task)
		if taskDesc == "" {
			return nil, errors.New(domaintool.RuntimeMessagef("todo_empty_desc", "task at index %d has empty description", i))
		}
		items[i].Task = taskDesc

		normalizedStatus, valid := normalizeTodoStatus(item.Status)
		if !valid {
			return nil, errors.New(domaintool.RuntimeMessagef("todo_invalid_status", "task '%s' has invalid status '%s'. Must be 'pending', 'in_progress', or 'completed'", cleanID, item.Status))
		}
		items[i].Status = normalizedStatus
		if normalizedStatus == "in_progress" {
			inProgressCount++
		}
	}

	if inProgressCount > 1 {
		return nil, errors.New(domaintool.RuntimeMessagef("todo_policy_violation", "policy violation: exactly 1 task may be 'in_progress' at a time (found %d). Please mark previous tasks 'completed' or 'pending' before starting the next", inProgressCount))
	}

	return items, nil
}

func (t *todoTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var currentTasks []domaintool.TodoItem
	if ts, ok := ctx.(domaintool.TodoState); ok {
		currentTasks = ts.GetTodos()
	}
	if len(currentTasks) == 0 && len(t.items) > 0 {
		currentTasks = append([]domaintool.TodoItem(nil), t.items...)
	}

	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		if len(currentTasks) > 0 {
			return t.formatSummary(currentTasks), nil
		}
		return "", errors.New(domaintool.RuntimeMessage("todo_empty_list", "empty task list: please provide at least one task in 'tasks'"))
	}

	isJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
	var items []domaintool.TodoItem

	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &items)
	} else if strings.HasPrefix(trimmed, "{") {
		var container struct {
			Tasks    []domaintool.TodoItem `json:"tasks"`
			Todos    []domaintool.TodoItem `json:"todos"`
			Items    []domaintool.TodoItem `json:"items"`
			Plan     []domaintool.TodoItem `json:"plan"`
			Content  []domaintool.TodoItem `json:"content"`
			Data     []domaintool.TodoItem `json:"data"`
			List     []domaintool.TodoItem `json:"list"`
			TodoList []domaintool.TodoItem `json:"todo_list"`
		}
		_ = json.Unmarshal([]byte(trimmed), &container)
		items = firstNonEmptyList(container.Tasks, container.Todos, container.Items, container.Plan, container.Content, container.Data, container.List, container.TodoList)

		if len(items) == 0 {
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
				updated, listOut, isList, actionErr := t.applyActionObject(raw, currentTasks)
				if actionErr != nil {
					return "", actionErr
				}
				if isList {
					return listOut, nil
				}
				items = updated
			}
		}
	}

	if len(items) == 0 {
		if isJSON {
			return "", errors.New(domaintool.RuntimeMessage("todo_invalid_args", "invalid arguments for todo: expected JSON with 'tasks' array or valid action parameters"))
		}
		items = parseMarkdownTodoLines(trimmed)
	}

	if len(items) == 0 {
		return "", errors.New(domaintool.RuntimeMessage("todo_empty_list", "empty task list: please provide at least one task in 'tasks'"))
	}

	validated, err := validateTodoItems(items)
	if err != nil {
		return "", err
	}

	t.items = append([]domaintool.TodoItem(nil), validated...)
	if ts, ok := ctx.(domaintool.TodoState); ok {
		if err := ts.SetTodos(validated); err != nil {
			return "", fmt.Errorf(domaintool.RuntimeMessage("todo_state_store_failed", "failed to store todo state: %w"), err)
		}
	}

	return t.formatSummary(validated), nil
}

func (t *todoTool) formatSummary(items []domaintool.TodoItem) string {
	var inProgressCount, pendingCount, completedCount int
	for _, item := range items {
		switch item.Status {
		case "in_progress":
			inProgressCount++
		case "completed":
			completedCount++
		default:
			pendingCount++
		}
	}

	var sb strings.Builder
	sb.WriteString(domaintool.RuntimeMessagef("todo_summary_line", "Updated todo plan: %d pending, %d in progress, %d completed.\n\n", pendingCount, inProgressCount, completedCount))
	for _, item := range items {
		marker := domaintool.RuntimeMessage("todo_marker_pending", "[ ]")
		switch item.Status {
		case "completed":
			marker = domaintool.RuntimeMessage("todo_marker_completed", "[x]")
		case "in_progress":
			marker = domaintool.RuntimeMessage("todo_marker_in_progress", "[>]")
		}
		sb.WriteString(domaintool.RuntimeMessagef("todo_item_line", "%s %s: %s\n", marker, item.ID, item.Task))
	}
	return sb.String()
}