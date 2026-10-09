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
}
