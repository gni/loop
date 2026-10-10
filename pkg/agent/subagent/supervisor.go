package subagent

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MaxSubagentDepth defines the maximum allowable nesting depth for subagents.
const MaxSubagentDepth = 4

// MaxTrackedTasks bounds the supervisor registry. Without a cap every delegation adds
// an entry that lives for the process lifetime, and the poll loop takes the lock on
// every tick.
const MaxTrackedTasks = 512

// TaskRetentionGrace keeps recently finished tasks readable: pollers look up a task
// right after it completes, so eviction must not clear entries that are still fresh.
const TaskRetentionGrace = time.Minute

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
// MaxTrackedTasks, so a long-lived process cannot grow the map without bound.
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
	s.evictLocked()
}

func (s *TaskSupervisor) evictLocked() {
	if len(s.tasks) <= MaxTrackedTasks {
		return
	}

	var terminal []*SubagentTask
	now := time.Now()
	for _, t := range s.tasks {
		if (t.Status == "completed" || t.Status == "failed") && now.Sub(t.UpdatedAt) > TaskRetentionGrace {
			terminal = append(terminal, t)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].UpdatedAt.Before(terminal[j].UpdatedAt) })

	for _, t := range terminal {
		if len(s.tasks) <= MaxTrackedTasks {
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
	task.Status = status
	task.Response = response
	if err != nil {
		task.Error = err.Error()
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
	if cfgMax > MaxSubagentDepth {
		return MaxSubagentDepth
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

// CheckDepthAllowed is the ceiling-only form for callers with no config value.
func CheckDepthAllowed(currentDepth int) error {
	return MaxDepthAllowed(MaxSubagentDepth, currentDepth)
}
