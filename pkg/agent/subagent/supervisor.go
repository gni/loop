package subagent

import (
	"fmt"
	"sync"
	"time"
)

// MaxSubagentDepth defines the maximum allowable nesting depth for subagents.
const MaxSubagentDepth = 4

// TaskSupervisor coordinates task execution tracking across subagents.
type TaskSupervisor struct {
	tasks map[string]*SubagentTask
	mu    sync.RWMutex
}

// NewTaskSupervisor creates a thread-safe task supervisor.
func NewTaskSupervisor() *TaskSupervisor {
	return &TaskSupervisor{
		tasks: make(map[string]*SubagentTask),
	}
}

// RegisterTask adds a newly spawned task with "pending" status.
func (s *TaskSupervisor) RegisterTask(id, agentName, prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[id] = &SubagentTask{
		ID:        id,
		AgentName: agentName,
		Prompt:    prompt,
		Status:    "pending",
		UpdatedAt: time.Now(),
	}
}

// GetTask returns a copy of a tracked task by ID.
func (s *TaskSupervisor) GetTask(id string) (*SubagentTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, exists := s.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task '%s' not found", id)
	}
	taskCopy := *task
	return &taskCopy, nil
}

// UpdateTaskStatus records state changes, output responses, or errors.
func (s *TaskSupervisor) UpdateTaskStatus(id, status, response string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, exists := s.tasks[id]
	if !exists {
		return
	}
	task.Status = status
	task.Response = response
	if err != nil {
		task.Error = err.Error()
	}
	task.UpdatedAt = time.Now()
}

// CheckDepthAllowed returns an error if creating another subagent at the given depth would exceed MaxSubagentDepth.
func CheckDepthAllowed(currentDepth int) error {
	if currentDepth >= MaxSubagentDepth {
		return fmt.Errorf("maximum subagent nesting depth (%d) reached", MaxSubagentDepth)
	}
	return nil
}
