package tool

import (
	"loop/pkg/agent/tool/todo"
	domaintool "loop/pkg/domain/tool"
)

// TodoItem represents an individual task in the agent's plan.
type TodoItem = domaintool.TodoItem

// TodoState is an interface implemented by agent contexts that maintain a task board.
type TodoState = domaintool.TodoState

// NewTodoTool creates a new instance of the todo planning tool.
func NewTodoTool() ToolExecutor {
	return todo.NewTodoTool()
}
