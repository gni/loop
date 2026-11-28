package todo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	domaintool "loop/pkg/domain/tool"
)

type todoTool struct {
	mu    sync.Mutex
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
			Description: domaintool.FormatToolDescription("todo", "Track a task plan. Prefer markdown for full plans, especially long lists, so tasks can stream as text. Use one checklist row per task: '- [ ] task-1: Describe work'. Use [>] for the one active task and [x] for completed tasks. Structured tasks arrays remain supported. For action=update or remove, always provide task_id. Action=list requires no task_id. At most one task may be in_progress."),
			Parameters: domaintool.JSONSchema{
				Type: "object",
				Properties: map[string]domaintool.SchemaProp{
					"markdown": domaintool.StringProp(domaintool.FormatParamDescription("todo", "markdown", "Complete plan as a newline-separated checklist string. Prefer this for full plans so tasks appear while generated. Each line must have a stable ID and description: - [ ] task-1: Describe work. IDs use letters, digits, underscore, dot or hyphen. [ ] pending, [>] in_progress, [x] completed. Send markdown OR tasks, not both; use action for individual updates.")),
					"tasks": {
						Type:        "array",
						Description: domaintool.FormatParamDescription("todo", "tasks", "The complete updated list of tasks representing the active plan."),
						Items: &domaintool.SchemaProp{
							Type: "object",
							Properties: map[string]domaintool.SchemaProp{
								"id":     domaintool.StringProp(domaintool.FormatParamDescription("todo", "id", "Unique identifier for the task (e.g., 'task-1', 'step-1').")),
								"task":   domaintool.StringProp(domaintool.FormatParamDescription("todo", "task", "Clear, concrete description of the task.")),
								"status": {Type: "string", Description: domaintool.FormatParamDescription("todo", "status", "Task status: pending, in_progress, or completed. At most one task may be in_progress."), Enum: []string{"pending", "in_progress", "completed"}},
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
	return domaintool.FormatToolSnippet(t.Name(), "Manage a plan. Prefer {\"markdown\":\"- [ ] task-1: Describe work\\n- [>] task-2: Active work\"} for full plans; actions update individual tasks. At most one task in_progress.")
}

func (t *todoTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Task Planning: For complex multi-step objectives, use the 'todo' tool to initialize and update your plan. Mark the active task 'in_progress' and check off tasks as 'completed'.",
		"Use the markdown string for full plan creation or replacement, especially long lists, so each task can appear as it is generated. Include a stable task ID before the colon on every checklist row and preserve those IDs on later replacements. Use action and task_id for individual status updates.",
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
			if after, ok := strings.CutPrefix(s, prefix); ok {
				return after
			}
		}
		return s
	}
	st, si := strip(tgt), strip(itm)
	return st == si || si == tgt || st == itm
}

func findExactTaskID(tasks []domaintool.TodoItem, targetID string) int {
	trimmed := strings.TrimSpace(targetID)
	if trimmed == "" {
		return -1
	}
	for i, item := range tasks {
		if strings.EqualFold(trimmed, strings.TrimSpace(item.ID)) {
			return i
		}
	}
	return -1
}

func findTaskIndex(tasks []domaintool.TodoItem, targetID string) (int, error) {
	trimmed := strings.TrimSpace(targetID)
	if trimmed == "" {
		return -1, nil
	}
	for i, item := range tasks {
		if strings.EqualFold(trimmed, strings.TrimSpace(item.ID)) {
			return i, nil
		}
	}
	matched := -1
	for i, it := range tasks {
		if matchTaskID(trimmed, it.ID, i) {
			if matched >= 0 {
				return -1, fmt.Errorf("task ID %q is ambiguous; use an exact ID from the plan", targetID)
			}
			matched = i
		}
	}
	if matched >= 0 {
		return matched, nil
	}
	// Fallback: match by exact case-insensitive task description if unambiguous.
	for i, it := range tasks {
		if strings.EqualFold(trimmed, strings.TrimSpace(it.Task)) {
			if matched >= 0 {
				return -1, fmt.Errorf("task %q matches multiple items; use an exact ID from the plan", targetID)
			}
			matched = i
		}
	}
	return matched, nil
}

func availableTaskIDs(tasks []domaintool.TodoItem) string {
	if len(tasks) == 0 {
		return ""
	}
	ids := make([]string, 0, len(tasks))
	for _, it := range tasks {
		ids = append(ids, it.ID)
	}
	return strings.Join(ids, ", ")
}

func nextTaskID(items []domaintool.TodoItem) string {
	used := make(map[string]bool, len(items))
	for _, item := range items {
		used[strings.ToLower(strings.TrimSpace(item.ID))] = true
	}
	for number := len(items) + 1; ; number++ {
		id := fmt.Sprintf("task-%d", number)
		if !used[id] {
			return id
		}
	}
}

func demoteOtherInProgress(tasks []domaintool.TodoItem, activeIdx int) {
	for i := range tasks {
		if i != activeIdx && strings.EqualFold(tasks[i].Status, "in_progress") {
			tasks[i].Status = "pending"
		}
	}
}

func normalizeTodoStatus(raw string) (string, bool) {
	return domaintool.NormalizeTodoStatus(raw)
}

func resolveTodoAction(action, status, taskID, taskDesc string) (string, string) {
	switch action {
	case "complete", "completed", "finish", "finished", "done", "check":
		return "update", domaintool.FirstNonEmpty(status, "completed")
	case "start", "begin", "in_progress", "in-progress", "inprogress", "active", "working":
		return "update", domaintool.FirstNonEmpty(status, "in_progress")
	case "pending", "reopen", "uncheck", "todo":
		return "update", domaintool.FirstNonEmpty(status, "pending")
	case "reset":
		if taskID != "" {
			return "update", domaintool.FirstNonEmpty(status, "pending")
		}
		return "clear", status
	case "clear", "empty", "wipe", "delete_all", "remove_all":
		return "clear", status
	case "delete", "del", "rm", "drop":
		return "remove", status
	case "create", "insert", "append", "new", "push":
		return "add", status
	case "modify", "edit", "change", "set", "mark", "set_status", "upsert":
		return "update", status
	case "list", "get", "show", "view", "read", "status", "inspect":
		return "list", status
	case "":
		if taskID != "" && (status != "" || taskDesc != "") {
			return "update", status
		}
	}
	return action, status
}

func (t *todoTool) applyActionObject(raw map[string]any, currentTasks []domaintool.TodoItem) (items []domaintool.TodoItem, listOutput string, isList bool, err error) {
	rawAction := strings.ToLower(domaintool.ExtractMapString(raw, "action", "op", "operation", "command", "type"))
	taskID, taskDesc, rawStatus := domaintool.ExtractTodoFields(raw)
	rawStatus = strings.ToLower(rawStatus)

	action, status := resolveTodoAction(rawAction, rawStatus, taskID, taskDesc)
	if status != "" {
		if norm, ok := normalizeTodoStatus(status); ok {
			status = norm
		}
	}

	switch action {
	case "list":
		if len(currentTasks) == 0 {
			return nil, "No active todo plan.", true, nil
		}
		return nil, t.formatSummary(currentTasks), true, nil

	case "clear":
		return []domaintool.TodoItem{}, "", false, nil

	case "update":
		if len(currentTasks) == 0 {
			if taskDesc != "" {
				id := domaintool.FirstNonEmpty(taskID, "task-1")
				st := domaintool.FirstNonEmpty(status, "pending")
				return []domaintool.TodoItem{{ID: id, Task: taskDesc, Status: st}}, "", false, nil
			}
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_no_plan_to_update", "cannot update task: no active todo plan exists. Initialize a plan first with markdown (e.g. {\"markdown\":\"- [ ] task-1: Describe work\"})"))
		}
		if taskID == "" && status == "" && taskDesc == "" {
			return nil, t.formatSummary(currentTasks), true, nil
		}
		if taskID == "" && len(currentTasks) == 1 {
			taskID = currentTasks[0].ID
		}
		if taskID == "" && status == "completed" {
			for _, item := range currentTasks {
				if item.Status == "in_progress" {
					taskID = item.ID
					break
				}
			}
		}
		if taskID == "" && status == "in_progress" {
			for _, item := range currentTasks {
				if item.Status == "pending" {
					taskID = item.ID
					break
				}
			}
		}
		if taskID == "" {
			return nil, "", false, fmt.Errorf("%s (available IDs: %s)", domaintool.RuntimeMessage("todo_missing_task_id", "update is ambiguous: provide task_id or send the full markdown plan"), availableTaskIDs(currentTasks))
		}
		matchedIdx, err := findTaskIndex(currentTasks, taskID)
		if err != nil {
			return nil, "", false, err
		}
		if matchedIdx == -1 {
			if taskDesc != "" {
				st := domaintool.FirstNonEmpty(status, "pending")
				if strings.EqualFold(st, "in_progress") {
					demoteOtherInProgress(currentTasks, -1)
				}
				return append(currentTasks, domaintool.TodoItem{ID: taskID, Task: taskDesc, Status: st}), "", false, nil
			}
			return nil, "", false, fmt.Errorf("%s (available IDs: %s)", fmt.Sprintf(domaintool.RuntimeMessage("todo_task_not_found", "task '%s' not found in current plan"), taskID), availableTaskIDs(currentTasks))
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
			return nil, "", false, fmt.Errorf("%s (available IDs: %s)", domaintool.RuntimeMessage("todo_missing_task_id", "task_id is required to remove a task"), availableTaskIDs(currentTasks))
		}
		matchedIdx, err := findTaskIndex(currentTasks, taskID)
		if err != nil {
			return nil, "", false, err
		}
		if matchedIdx == -1 {
			return nil, "", false, fmt.Errorf("%s (available IDs: %s)", fmt.Sprintf(domaintool.RuntimeMessage("todo_task_not_found", "task '%s' not found in current plan"), taskID), availableTaskIDs(currentTasks))
		}
		updated := make([]domaintool.TodoItem, 0, len(currentTasks)-1)
		for i, it := range currentTasks {
			if i != matchedIdx {
				updated = append(updated, it)
			}
		}
		return updated, "", false, nil

	case "add":
		if taskDesc == "" {
			return nil, "", false, errors.New(domaintool.RuntimeMessage("todo_missing_description", "task description is required to add a task"))
		}
		if taskID != "" {
			if matchedIdx := findExactTaskID(currentTasks, taskID); matchedIdx != -1 {
				currentTasks[matchedIdx].Task = taskDesc
				if status != "" {
					currentTasks[matchedIdx].Status = status
				}
				if strings.EqualFold(currentTasks[matchedIdx].Status, "in_progress") {
					demoteOtherInProgress(currentTasks, matchedIdx)
				}
				return currentTasks, "", false, nil
			}
		}
		if taskID == "" {
			taskID = nextTaskID(currentTasks)
		}
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
			i, err := findTaskIndex(currentTasks, taskID)
			if err != nil {
				return nil, "", false, err
			}
			if i != -1 {
				currentTasks[i].Task = taskDesc
				if status != "" {
					currentTasks[i].Status = status
				}
				if strings.EqualFold(currentTasks[i].Status, "in_progress") {
					demoteOtherInProgress(currentTasks, i)
				}
				return currentTasks, "", false, nil
			}
		}
		id := domaintool.FirstNonEmpty(taskID, nextTaskID(currentTasks))
		st := domaintool.FirstNonEmpty(status, "pending")
		if strings.EqualFold(st, "in_progress") {
			demoteOtherInProgress(currentTasks, -1)
		}
		return append(currentTasks, domaintool.TodoItem{ID: id, Task: taskDesc, Status: st}), "", false, nil
	}
}

func validateTodoItems(items []domaintool.TodoItem) ([]domaintool.TodoItem, error) {
	seenIDs := make(map[string]bool, len(items))
	inProgressCount := 0
	filtered := make([]domaintool.TodoItem, 0, len(items))

	for i, item := range items {
		taskDesc := strings.TrimSpace(item.Task)
		if taskDesc == "" {
			return nil, errors.New(domaintool.RuntimeMessagef("todo_empty_desc", "task at index %d has empty description", i))
		}
		cleanID := domaintool.FirstNonEmpty(strings.TrimSpace(item.ID), fmt.Sprintf("task-%d", len(filtered)+1))
		key := strings.ToLower(cleanID)
		if seenIDs[key] {
			cleanID = nextTaskID(filtered)
			key = strings.ToLower(cleanID)
		}
		seenIDs[key] = true

		normalizedStatus, valid := normalizeTodoStatus(item.Status)
		if !valid {
			return nil, errors.New(domaintool.RuntimeMessagef("todo_invalid_status", "task '%s' has invalid status '%s'. Must be 'pending', 'in_progress', or 'completed'", cleanID, item.Status))
		}
		if normalizedStatus == "in_progress" {
			inProgressCount++
		}
		filtered = append(filtered, domaintool.TodoItem{
			ID:     cleanID,
			Task:   taskDesc,
			Status: normalizedStatus,
		})
	}

	if inProgressCount > 1 {
		// Auto-resolve: keep the last in_progress task active and demote earlier ones to pending.
		lastIdx := -1
		for i := len(filtered) - 1; i >= 0; i-- {
			if filtered[i].Status == "in_progress" {
				if lastIdx == -1 {
					lastIdx = i
				} else {
					filtered[i].Status = "pending"
				}
			}
		}
	}

	return filtered, nil
}

func (t *todoTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var currentTasks []domaintool.TodoItem
	if ts, ok := ctx.(domaintool.TodoState); ok {
		currentTasks = append([]domaintool.TodoItem(nil), ts.GetTodos()...)
	} else {
		currentTasks = append([]domaintool.TodoItem(nil), t.items...)
	}

	validated, listOutput, err := t.preparePlan(currentTasks, arguments)
	if err != nil {
		return "", err
	}
	if validated == nil {
		return listOutput, nil
	}

	if ts, ok := ctx.(domaintool.TodoState); ok {
		if err := ts.SetTodos(validated); err != nil {
			return "", fmt.Errorf(domaintool.RuntimeMessage("todo_state_store_failed", "failed to store todo state: %w"), err)
		}
	} else {
		t.items = append([]domaintool.TodoItem(nil), validated...)
	}

	return t.formatCounts(validated), nil
}

// ReplayPlan applies saved todo arguments to a plan without executing any other
// tools or touching the filesystem. Failed calls leave the supplied plan intact.
func ReplayPlan(current []domaintool.TodoItem, arguments string) ([]domaintool.TodoItem, error) {
	original := append([]domaintool.TodoItem(nil), current...)
	updated, _, err := (&todoTool{}).preparePlan(append([]domaintool.TodoItem(nil), current...), arguments)
	if err != nil {
		return original, err
	}
	if updated == nil {
		return original, nil
	}
	return updated, nil
}

func isTodoFullPlanAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "update", "replace", "set", "create", "init", "write", "save", "sync", "plan", "modify", "edit", "upsert", "overwrite", "put":
		return true
	default:
		return false
	}
}

func isTodoAddAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "add", "append", "insert", "push", "new":
		return true
	default:
		return false
	}
}

func appendTodoItems(existing, additions []domaintool.TodoItem) []domaintool.TodoItem {
	merged := append([]domaintool.TodoItem(nil), existing...)
	for _, add := range additions {
		id := strings.TrimSpace(add.ID)
		if id != "" {
			if idx := findExactTaskID(merged, id); idx != -1 {
				// Avoid colliding with an existing auto-generated ID when adding new lines.
				if strings.HasPrefix(strings.ToLower(id), "task-") {
					add.ID = nextTaskID(merged)
				} else {
					merged[idx].Task = add.Task
					if add.Status != "" {
						merged[idx].Status = add.Status
					}
					if strings.EqualFold(merged[idx].Status, "in_progress") {
						demoteOtherInProgress(merged, idx)
					}
					continue
				}
			}
		} else {
			add.ID = nextTaskID(merged)
		}
		if strings.EqualFold(add.Status, "in_progress") {
			demoteOtherInProgress(merged, -1)
		}
		merged = append(merged, add)
	}
	return merged
}

func hasNonEmptyScalar(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			return strings.TrimSpace(s) != ""
		}
	}
	return true
}

func (t *todoTool) preparePlan(currentTasks []domaintool.TodoItem, arguments string) ([]domaintool.TodoItem, string, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		if len(currentTasks) > 0 {
			return nil, t.formatSummary(currentTasks), nil
		}
		return nil, "No active todo plan.", nil
	}

	isJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
	var items []domaintool.TodoItem

	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, "", invalidArguments(err)
		}
	} else if strings.HasPrefix(trimmed, "{") {
		if !json.Valid([]byte(trimmed)) {
			return nil, "", invalidArguments(errors.New("malformed JSON"))
		}
		var container struct {
			Markdown *string               `json:"markdown"`
			Action   string                `json:"action"`
			TaskID   json.RawMessage       `json:"task_id"`
			ID       json.RawMessage       `json:"id"`
			Tasks    []domaintool.TodoItem `json:"tasks"`
			Todos    []domaintool.TodoItem `json:"todos"`
			Items    []domaintool.TodoItem `json:"items"`
			Plan     []domaintool.TodoItem `json:"plan"`
			Content  json.RawMessage       `json:"content"`
			Data     []domaintool.TodoItem `json:"data"`
			List     []domaintool.TodoItem `json:"list"`
			TodoList []domaintool.TodoItem `json:"todo_list"`
			Actions  []domaintool.TodoItem `json:"actions"`
		}
		if err := json.Unmarshal([]byte(trimmed), &container); err != nil {
			return nil, "", invalidArguments(err)
		}
		var contentItems []domaintool.TodoItem
		if rawContent := bytes.TrimSpace(container.Content); len(rawContent) > 0 && !bytes.Equal(rawContent, []byte("null")) {
			switch rawContent[0] {
			case '[':
				if err := json.Unmarshal(rawContent, &contentItems); err != nil {
					return nil, "", invalidArguments(err)
				}
			case '"':
				// String "content" is a task description alias handled by applyActionObject.
			default:
				if err := json.Unmarshal(rawContent, &contentItems); err != nil {
					return nil, "", invalidArguments(err)
				}
			}
		}
		lists := [][]domaintool.TodoItem{container.Tasks, container.Todos, container.Items, container.Plan, contentItems, container.Data, container.List, container.TodoList, container.Actions}
		hasExplicitEmptyList := false
		for _, list := range lists {
			if list != nil {
				if len(list) == 0 {
					hasExplicitEmptyList = true
					continue
				}
				if len(items) > 0 {
					return nil, "", invalidArguments(errors.New("provide only one plan list"))
				}
				items = list
			}
		}
		hasMarkdown := container.Markdown != nil && strings.TrimSpace(*container.Markdown) != ""
		if hasMarkdown {
			hasTargetID := hasNonEmptyScalar(container.TaskID) || hasNonEmptyScalar(container.ID)
			actionTrimmed := strings.TrimSpace(container.Action)
			conflictingAction := actionTrimmed != "" && !isTodoFullPlanAction(actionTrimmed) && !isTodoAddAction(actionTrimmed)
			if len(items) > 0 || hasTargetID || conflictingAction {
				return nil, "", invalidArguments(errors.New("provide markdown, tasks, or an action separately"))
			}
			parsed, err := parseMarkdownPlan(*container.Markdown)
			if err != nil {
				return nil, "", err
			}
			if isTodoAddAction(actionTrimmed) && len(currentTasks) > 0 {
				items = appendTodoItems(currentTasks, parsed)
			} else {
				items = parsed
			}
		} else if len(items) > 0 && isTodoAddAction(container.Action) && len(currentTasks) > 0 {
			items = appendTodoItems(currentTasks, items)
		}

		if len(items) == 0 {
			var raw map[string]any
			if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
				updated, listOut, isList, actionErr := t.applyActionObject(raw, currentTasks)
				if actionErr != nil {
					return nil, "", actionErr
				}
				if isList {
					return nil, listOut, nil
				}
				if updated != nil {
					items = updated
				} else if hasExplicitEmptyList {
					items = []domaintool.TodoItem{}
				} else {
					action := domaintool.ExtractMapString(raw, "action", "op", "operation", "command", "type")
					id, description, status := domaintool.ExtractTodoFields(raw)
					if action != "" || id != "" || description != "" || status != "" {
						return nil, "", invalidArguments(errors.New("missing valid action parameters"))
					}
				}
			}
		}
	}

	if items == nil {
		if isJSON {
			return nil, "", invalidArguments(errors.New("missing markdown checklist, tasks array, or valid action parameters"))
		}
		items = domaintool.ParseMarkdownTodoLines(trimmed, "task-")
	}

	if len(items) == 0 {
		if isJSON && items != nil {
			return items, "", nil
		}
		return nil, "", errors.New(domaintool.RuntimeMessage("todo_empty_list", "empty task list: provide at least one checklist line in markdown or task in tasks"))
	}

	validated, err := validateTodoItems(items)
	if err != nil {
		return nil, "", err
	}

	return validated, "", nil
}

func parseMarkdownPlan(markdown string) ([]domaintool.TodoItem, error) {
	items := domaintool.ParseMarkdownTodoLines(markdown, "task-")
	if len(items) == 0 {
		return nil, invalidArguments(errors.New("markdown plan is empty"))
	}
	return items, nil
}

func invalidArguments(reason error) error {
	return errors.New(domaintool.RuntimeMessagef("todo_invalid_args", "invalid todo arguments: %v. Use {\"markdown\":\"- [ ] task-1: Describe work\"} or {\"action\":\"update\",\"task_id\":\"task-1\",\"status\":\"completed\"}.", reason))
}

func (t *todoTool) formatCounts(items []domaintool.TodoItem) string {
	pending, active, completed := 0, 0, 0
	for _, item := range items {
		switch item.Status {
		case "completed":
			completed++
		case "in_progress":
			active++
		default:
			pending++
		}
	}
	return fmt.Sprintf("Plan updated: %d pending, %d active, %d completed.", pending, active, completed)
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
