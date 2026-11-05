package swarm

import "loop/pkg/agent/tool"

func (ma *MultiAgent) GetTodos() []tool.TodoItem {
	ma.planMu.RLock()
	defer ma.planMu.RUnlock()
	return append([]tool.TodoItem(nil), ma.todos...)
}

func (ma *MultiAgent) SetTodos(todos []tool.TodoItem) error {
	ma.planMu.Lock()
	ma.todos = append([]tool.TodoItem(nil), todos...)
	ma.planMu.Unlock()
	if ma.Manager != nil {
		return ma.Manager.SaveAgentState(ma, "running")
	}
	return nil
}
