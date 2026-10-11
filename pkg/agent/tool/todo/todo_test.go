package todo

import (
	"context"
	"io"
	"strings"
	"testing"

	domaintool "loop/pkg/domain/tool"
)

type mockTodoContext struct {
	todos []domaintool.TodoItem
}

func (m *mockTodoContext) SafePath(inputPath string) (string, error) {
	return inputPath, nil
}
func (m *mockTodoContext) GetWorkspaceRoot() string {
	return "."
}
func (m *mockTodoContext) GetActiveSkills() []domaintool.Skill {
	return nil
}
func (m *mockTodoContext) ReloadSkills() []domaintool.Skill {
	return nil
}
func (m *mockTodoContext) SpawnTask(command string, w io.Writer) (string, error) {
	return "", nil
}
func (m *mockTodoContext) GetTaskStatus(taskID string) (string, string, error) {
	return "", "", nil
}
func (m *mockTodoContext) KillTask(taskID string) error {
	return nil
}
func (m *mockTodoContext) Context() context.Context {
	return context.Background()
}
func (m *mockTodoContext) HasSubagent(name string) bool {
	return false
}

func (m *mockTodoContext) GetTodos() []domaintool.TodoItem {
	return m.todos
}

func (m *mockTodoContext) SetTodos(todos []domaintool.TodoItem) error {
	m.todos = todos
	return nil
}

func TestTodoTool(t *testing.T) {
	tt := NewTodoTool()
	mctx := &mockTodoContext{}

	// 1. Valid plan initialization
	jsonInput := `{
		"tasks": [
			{"id": "1", "task": "Design architecture", "status": "completed"},
			{"id": "2", "task": "Write implementation", "status": "in_progress"},
			{"id": "3", "task": "Run tests", "status": "pending"}
		]
	}`
	out, err := tt.Execute(mctx, jsonInput)
	if err != nil {
		t.Fatalf("unexpected error executing todo: %v", err)
	}
	if !strings.Contains(out, "1 pending, 1 in progress, 1 completed") {
		t.Errorf("expected counts summary, got: %s", out)
	}
	if !strings.Contains(out, "[x] 1: Design architecture") {
		t.Errorf("expected completed item marker, got: %s", out)
	}
	if !strings.Contains(out, "[>] 2: Write implementation") {
		t.Errorf("expected in_progress item marker, got: %s", out)
	}
	if !strings.Contains(out, "[ ] 3: Run tests") {
		t.Errorf("expected pending item marker, got: %s", out)
	}

	// 2. Reject multiple in_progress tasks
	multiInProgress := `{
		"tasks": [
			{"id": "1", "task": "Task 1", "status": "in_progress"},
			{"id": "2", "task": "Task 2", "status": "in_progress"}
		]
	}`
	_, err = tt.Execute(mctx, multiInProgress)
	if err == nil {
		t.Errorf("expected error for multiple in_progress tasks, got nil")
	}

	// 3. Reject duplicate task IDs
	duplicateID := `{
		"tasks": [
			{"id": "same", "task": "Task 1", "status": "completed"},
			{"id": "same", "task": "Task 2", "status": "pending"}
		]
	}`
	_, err = tt.Execute(mctx, duplicateID)
	if err == nil {
		t.Errorf("expected error for duplicate task IDs, got nil")
	}

	// 4. Reject empty description
	emptyDesc := `{
		"tasks": [
			{"id": "1", "task": "", "status": "pending"}
		]
	}`
	_, err = tt.Execute(mctx, emptyDesc)
	if err == nil {
		t.Errorf("expected error for empty task description, got nil")
	}

	// 5. Support "content" as field name in task items
	contentFieldInput := `{
		"tasks": [
			{"id": "1", "content": "Backend config and domain model", "status": "in_progress"},
			{"id": "2", "content": "Application ports and use cases", "status": "pending"}
		]
	}`
	out, err = tt.Execute(mctx, contentFieldInput)
	if err != nil {
		t.Fatalf("unexpected error with content field: %v", err)
	}
	if !strings.Contains(out, "Backend config and domain model") {
		t.Errorf("expected parsed content description, got: %s", out)
	}

	// 6. Support "content" as top-level array key
	contentArrayInput := `{
		"content": [
			{"id": "1", "content": "Backend config and domain model", "status": "in_progress"},
			{"id": "2", "content": "Application ports and use cases", "status": "pending"}
		]
	}`
	out, err = tt.Execute(mctx, contentArrayInput)
	if err != nil {
		t.Fatalf("unexpected error with content top-level array: %v", err)
	}
	if !strings.Contains(out, "Backend config and domain model") {
		t.Errorf("expected parsed content description, got: %s", out)
	}

	// 7. Support markdown bullet lines
	markdownInput := `- [>] Task 1
- [ ] Task 2
- [x] Task 3`
	out, err = tt.Execute(mctx, markdownInput)
	if err != nil {
		t.Fatalf("unexpected error with markdown input: %v", err)
	}
	if !strings.Contains(out, "1 pending, 1 in progress, 1 completed") {
		t.Errorf("expected parsed markdown summary, got: %s", out)
	}

	// 8. Action: update existing task status
	updateAction := `{"action":"update","status":"completed","task_id":"task-1"}`
	out, err = tt.Execute(mctx, updateAction)
	if err != nil {
		t.Fatalf("unexpected error executing action update: %v", err)
	}
	if !strings.Contains(out, "[x] task-1: Task 1") {
		t.Errorf("expected task-1 to be marked completed, got: %s", out)
	}
	if !strings.Contains(out, "[ ] task-2: Task 2") {
		t.Errorf("expected task-2 to remain pending, got: %s", out)
	}

	// 9. Action: update task to in_progress (auto-demotes other in_progress tasks)
	startTask2 := `{"action":"update","status":"in_progress","task_id":"task-2"}`
	out, err = tt.Execute(mctx, startTask2)
	if err != nil {
		t.Fatalf("unexpected error executing in_progress update: %v", err)
	}
	if !strings.Contains(out, "[>] task-2: Task 2") {
		t.Errorf("expected task-2 to be in_progress, got: %s", out)
	}

	// 10. Update without action keyword (task_id + status)
	completeTask2 := `{"task_id":"task-2","status":"completed"}`
	out, err = tt.Execute(mctx, completeTask2)
	if err != nil {
		t.Fatalf("unexpected error executing implicit update: %v", err)
	}
	if !strings.Contains(out, "[x] task-2: Task 2") {
		t.Errorf("expected task-2 to be completed, got: %s", out)
	}

	// 11. Update matching numeric index ID "3"
	updateByIndex := `{"id":"3","status":"in_progress"}`
	out, err = tt.Execute(mctx, updateByIndex)
	if err != nil {
		t.Fatalf("unexpected error updating by index ID: %v", err)
	}
	if !strings.Contains(out, "[>] task-3: Task 3") {
		t.Errorf("expected task-3 to match and be in_progress, got: %s", out)
	}

	// 12. Add task
	addTask := `{"action":"add","task":"Verify deployment","status":"pending"}`
	out, err = tt.Execute(mctx, addTask)
	if err != nil {
		t.Fatalf("unexpected error adding task: %v", err)
	}
	if !strings.Contains(out, "Verify deployment") {
		t.Errorf("expected newly added task in plan, got: %s", out)
	}

	// 13. Remove task
	removeTask := `{"action":"remove","task_id":"task-3"}`
	out, err = tt.Execute(mctx, removeTask)
	if err != nil {
		t.Fatalf("unexpected error removing task: %v", err)
	}
	if strings.Contains(out, "Task 3") {
		t.Errorf("expected task-3 to be removed, got: %s", out)
	}

	// 14. Invalid JSON does NOT fall through to line-splitting markdown
	invalidJSON := `{"unexpected_field": 123}`
	_, err = tt.Execute(mctx, invalidJSON)
	if err == nil {
		t.Errorf("expected error for unrecognized JSON arguments, got nil")
	}
}

