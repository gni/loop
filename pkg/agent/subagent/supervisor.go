package subagent

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"loop/pkg/domain/limits"
)

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

// RegisterTask adds a newly spawned task with "pending" status. Terminal entries
// (completed/failed) are evicted oldest-first once the registry exceeds
// limits.MaxSupervisedTasks(), so a long-lived process cannot grow the map without bound.
func (s *TaskSupervisor) RegisterTask(id, agentName, prompt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		return fmt.Errorf("subagent task ID cannot be empty")
	}
	if _, exists := s.tasks[id]; exists {
		return fmt.Errorf("subagent task %q already exists", id)
	}
	s.evictLocked(limits.MaxSupervisedTasks() - 1)
	if len(s.tasks) >= limits.MaxSupervisedTasks() {
		return fmt.Errorf("supervisor task limit (%d) reached; recently finished tasks are still retained", limits.MaxSupervisedTasks())
	}
	s.tasks[id] = &SubagentTask{
		ID:        id,
		AgentName: agentName,
		Prompt:    prompt,
		Status:    "pending",
		UpdatedAt: time.Now(),
	}
	return nil
}

func (s *TaskSupervisor) evictLocked(target int) {
	if len(s.tasks) <= target {
		return
	}

	var terminal []*SubagentTask
	now := time.Now()
	for _, t := range s.tasks {
		if (t.Status == "completed" || t.Status == "failed") && now.Sub(t.UpdatedAt) > limits.TaskRetentionGrace() {
			terminal = append(terminal, t)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		return terminal[i].UpdatedAt.Before(terminal[j].UpdatedAt)
	})
	for _, t := range terminal {
		if len(s.tasks) <= target {
			return
		}
		delete(s.tasks, t.ID)
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
	if task.Status == "completed" || task.Status == "failed" {
		return
	}
	task.Status = status
	task.Response = response
	if err != nil {
		task.Error = err.Error()
	} else {
		task.Error = ""
	}
	task.UpdatedAt = time.Now()
}

// EffectiveMaxDepth resolves the single source of truth for nesting depth. Config is
// authoritative; a config value of 0 keeps the documented behaviour (spawning disabled),
// and any value above the hard ceiling is clamped to it.
func EffectiveMaxDepth(cfgMax int) int {
	if cfgMax <= 0 {
		return 0
	}
	ceiling := limits.SubagentDepth()
	if cfgMax > ceiling {
		return ceiling
	}
	return cfgMax
}

func MaxDepthAllowed(cfgMax, currentDepth int) error {
	limit := EffectiveMaxDepth(cfgMax)
	if limit == 0 {
		return fmt.Errorf("subagent spawning is disabled (max depth 0); set max_subagent_depth to allow it")
	}
	if currentDepth >= limit {
		return fmt.Errorf("maximum subagent nesting depth (%d) reached", limit)
	}
	return nil
}
