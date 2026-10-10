package agent

import (
	"testing"
	"time"
)

func TestPruneFinishedTasksLockedCapsRegistry(t *testing.T) {
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		StreamingTask: "stream",
	}

	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < MaxTrackedTasks+20; i++ {
		id := "finished" + time.Now().Format("150405.000000") + string(rune('a'+i%26)) + string(rune('A'+i%26))
		a.Tasks[id] = &Task{ID: id, Status: "completed", EndTime: base.Add(time.Duration(i) * time.Second)}
	}
	a.Tasks["stream"] = &Task{ID: "stream", Status: "running"}

	a.pruneFinishedTasksLocked()

	if len(a.Tasks) > MaxTrackedTasks {
		t.Fatalf("registry grew to %d tasks, cap is %d", len(a.Tasks), MaxTrackedTasks)
	}
	if _, ok := a.Tasks["stream"]; !ok {
		t.Fatal("running task was evicted")
	}
	if a.StreamingTask != "stream" {
		t.Fatalf("streaming marker cleared while its task is still running: %q", a.StreamingTask)
	}

	// Evicting a streaming task must clear the marker so it cannot dangle.
	for i := 0; i < 5; i++ {
		a.Tasks["extra"+string(rune('a'+i))] = &Task{ID: "extra" + string(rune('a'+i)), Status: "completed", EndTime: base}
	}
	a.Tasks["stream"].Status = "completed"
	for i := 0; i < MaxTrackedTasks; i++ {
		a.Tasks["fill"+string(rune('a'+i%26))+string(rune('A'+i%26))] = &Task{ID: "fill", Status: "completed", EndTime: base.Add(time.Duration(i) * time.Second)}
	}
	a.pruneFinishedTasksLocked()
	if _, ok := a.Tasks["stream"]; ok {
		t.Fatal("completed streaming task should have been evicted")
	}
	if a.StreamingTask != "" {
		t.Fatalf("streaming marker not cleared after eviction: %q", a.StreamingTask)
	}
}
