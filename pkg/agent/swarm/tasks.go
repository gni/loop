package swarm

import (
	"loop/pkg/agent/subagent"
)

type SubagentTask = subagent.SubagentTask

func (mam *MultiAgentManager) getSupervisor() *subagent.TaskSupervisor {
	mam.mu.Lock()
	defer mam.mu.Unlock()
	if mam.Supervisor == nil {
		mam.Supervisor = subagent.NewTaskSupervisor()
	}
	return mam.Supervisor
}

func (mam *MultiAgentManager) RegisterTask(id string, agentName string, prompt string) {
	mam.getSupervisor().RegisterTask(id, agentName, prompt)
}

func (mam *MultiAgentManager) GetTask(id string) (*SubagentTask, error) {
	return mam.getSupervisor().GetTask(id)
}

func (mam *MultiAgentManager) UpdateTaskStatus(id string, status string, response string, err error) {
	mam.getSupervisor().UpdateTaskStatus(id, status, response, err)
}
