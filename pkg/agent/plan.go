package agent

import "loop/pkg/agent/tool"

func (a *Agent) GetTodos() []tool.TodoItem {
	if a == nil {
		return nil
	}
	a.TodosMu.RLock()
	defer a.TodosMu.RUnlock()
	res := make([]tool.TodoItem, len(a.Todos))
	copy(res, a.Todos)
	return res
}

func (a *Agent) SetTodos(todos []tool.TodoItem) error {
	if a == nil {
		return nil
	}
	a.TodosMu.Lock()
	a.Todos = make([]tool.TodoItem, len(todos))
	copy(a.Todos, todos)
	a.TodosMu.Unlock()
	a.RefreshPlanStatus()
	return nil
}

// RefreshPlanStatus synchronizes a newly attached UI with restored task state.
func (a *Agent) RefreshPlanStatus() {
	if a == nil || a.UI == nil {
		return
	}
	todos := a.GetTodos()
	completed := 0
	for _, item := range todos {
		if item.Status == "completed" {
			completed++
		}
	}
	a.UI.UpdatePlanStatus(completed, len(todos))
}
