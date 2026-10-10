package subagent

import (
	"testing"
	"time"
)

// backdate marks a terminal task as older than the retention grace window so eviction
// can reach it.
func backdate(s *TaskSupervisor, id string, age time.Duration) {
	s.mu.Lock()
	if t, ok := s.tasks[id]; ok {
		t.UpdatedAt = time.Now().Add(-age)
	}
	s.mu.Unlock()
}

func TestRegisterTaskEvictsStaleTerminalEntriesOverCap(t *testing.T) {
	s := NewTaskSupervisor()

	s.RegisterTask("active", "agent", "prompt")
	s.UpdateTaskStatus("active", "running", "", nil)

	// Fill the registry with terminal entries, then age them past the grace window.
	for i := 0; i < MaxTrackedTasks+10; i++ {
		id := "stale" + time.Now().Format("150405.000000") + string(rune('a'+i%26)) + string(rune('A'+i%26))
		s.RegisterTask(id, "agent", "prompt")
		s.UpdateTaskStatus(id, "completed", "done", nil)
		backdate(s, id, TaskRetentionGrace+time.Second)
	}

	if len(s.tasks) > MaxTrackedTasks {
		t.Fatalf("registry grew to %d entries, cap is %d", len(s.tasks), MaxTrackedTasks)
	}
	if _, err := s.GetTask("active"); err != nil {
		t.Fatalf("running task was evicted: %v", err)
	}

	// A fresh terminal entry must survive: pollers read tasks right after completion.
	s.RegisterTask("fresh", "agent", "prompt")
	s.UpdateTaskStatus("fresh", "completed", "done", nil)
	if _, err := s.GetTask("fresh"); err != nil {
		t.Fatalf("fresh terminal task was evicted: %v", err)
	}
}
