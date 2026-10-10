package todo

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	domaintool "loop/pkg/domain/tool"
)

type todoTool struct {
	mu sync.RWMutex
}

// NewTodoTool creates a new instance of the todo planning tool.
func NewTodoTool() domaintool.ToolExecutor {
	return &todoTool{}
}

func (t *todoTool) Name() string {
	return "todo"
}

func (t *todoTool) Definition() domaintool.Tool {
	return domaintool.Tool{
		Type: "function",
		Function: domaintool.FunctionDefinition{
			Name:        "todo",
			Description: domaintool.FormatToolDescription("todo", "Manage and track progress on multi-step tasks. Call this tool to initialize, update, or track your implementation plan. Send the entire updated list on every call. Exactly one task may be 'in_progress' at a time."),
			Parameters: domaintool.JSONSchema{
				Type: "object",
				Properties: map[string]domaintool.SchemaProp{
					"tasks": {
						Type:        "array",
						Description: domaintool.FormatParamDescription("todo", "tasks", "The complete updated list of tasks representing the active plan."),
						Items: &domaintool.SchemaProp{
							Type: "object",
							Properties: map[string]domaintool.SchemaProp{
								"id": {
									Type:        "string",
									Description: domaintool.FormatParamDescription("todo", "id", "Unique identifier for the task (e.g., 'task-1', 'step-1')."),
								},
								"task": {
									Type:        "string",
									Description: domaintool.FormatParamDescription("todo", "task", "Clear, concrete description of the task."),
								},
								"status": {
									Type:        "string",
									Description: domaintool.FormatParamDescription("todo", "status", "Task status. Exactly one task may be 'in_progress' at any given moment."),
									Enum:        []string{"pending", "in_progress", "completed"},
								},
							},
							Required: []string{"id", "task", "status"},
						},
					},
				},
				Required: []string{"tasks"},
			},
		},
	}
}

func (t *todoTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Manage structured implementation plan. Whole-list replacement; exactly one task in_progress.")
}

func (t *todoTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Task Planning: For complex multi-step objectives, use the 'todo' tool to initialize and update your plan. Mark the active task 'in_progress' and check off tasks as 'completed'.",
	})
}

func (t *todoTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Tasks []domaintool.TodoItem `json:"tasks"`
		Todos []domaintool.TodoItem `json:"todos"`
		Items []domaintool.TodoItem `json:"items"`
		Plan  []domaintool.TodoItem `json:"plan"`
	}

	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "[") {
		var list []domaintool.TodoItem
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
			args.Tasks = list
		}
	} else {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("invalid arguments for todo: %w. Expected JSON with {\"tasks\": [...]}", err)
		}
	}

	items := args.Tasks
	if len(items) == 0 && len(args.Todos) > 0 {
		items = args.Todos
	} else if len(items) == 0 && len(args.Items) > 0 {
		items = args.Items
	} else if len(items) == 0 && len(args.Plan) > 0 {
		items = args.Plan
	}

	if len(items) == 0 {
		return "", fmt.Errorf("empty task list: please provide at least one task in 'tasks'")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	seenIDs := make(map[string]bool)
	inProgressCount := 0
	pendingCount := 0
	completedCount := 0

	for i, item := range items {
		cleanID := strings.TrimSpace(item.ID)
		if cleanID == "" {
			cleanID = fmt.Sprintf("task-%d", i+1)
			items[i].ID = cleanID
		}
		if seenIDs[cleanID] {
			return "", fmt.Errorf("duplicate task id: '%s' appears more than once", cleanID)
		}
		seenIDs[cleanID] = true

		taskDesc := strings.TrimSpace(item.Task)
		if taskDesc == "" {
			return "", fmt.Errorf("task at index %d has empty description", i)
		}
		items[i].Task = taskDesc

		status := strings.ToLower(strings.TrimSpace(item.Status))
		switch status {
		case "in_progress", "inprogress", "active", "doing", "working":
			items[i].Status = "in_progress"
			inProgressCount++
		case "completed", "done", "finished", "success":
			items[i].Status = "completed"
			completedCount++
		case "pending", "todo", "waiting", "planned", "":
			items[i].Status = "pending"
			pendingCount++
		default:
			return "", fmt.Errorf("task '%s' has invalid status '%s'. Must be 'pending', 'in_progress', or 'completed'", cleanID, item.Status)
		}
	}

	// Single-active task discipline
	if inProgressCount > 1 {
		return "", fmt.Errorf("policy violation: exactly 1 task may be 'in_progress' at a time (found %d). Please mark previous tasks 'completed' or 'pending' before starting the next", inProgressCount)
	}

	// If the context supports TodoState, update it
	if ts, ok := ctx.(domaintool.TodoState); ok {
		if err := ts.SetTodos(items); err != nil {
			return "", fmt.Errorf("failed to store todo state: %w", err)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Updated todo plan: %d pending, %d in progress, %d completed.\n\n", pendingCount, inProgressCount, completedCount))
	for _, item := range items {
		marker := "[ ]"
		if item.Status == "completed" {
			marker = "[x]"
		} else if item.Status == "in_progress" {
			marker = "[>]"
		}
		sb.WriteString(fmt.Sprintf("%s %s: %s\n", marker, item.ID, item.Task))
	}

	return sb.String(), nil
}
