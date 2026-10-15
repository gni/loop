package agent

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestKillAllTasksAndClearTasks(t *testing.T) {
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		NextTaskId:    1,
	}

	// Spawn a background task using SpawnTask
	var buf bytes.Buffer
	taskID, err := a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}

	task := a.Tasks[taskID]
	task.mu.Lock()
	status := task.Status
	task.mu.Unlock()
	if status != "running" {
		t.Fatalf("expected status running, got %s", status)
	}

	if cmd := a.GetTaskCommand(taskID); cmd != "sleep 60" {
		t.Fatalf("expected GetTaskCommand to return 'sleep 60', got %q", cmd)
	}

	tasks := a.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in ListTasks, got %d", len(tasks))
	}

	// Test KillAllTasks
	killed := a.KillAllTasks()
	if killed != 1 {
		t.Fatalf("expected 1 task killed, got %d", killed)
	}

	task.mu.Lock()
	status = task.Status
	task.mu.Unlock()
	if status != "killed" {
		t.Fatalf("expected task status 'killed', got %s", status)
	}

	// Test ClearTasks
	// Add another task
	_, err = a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask 2 failed: %v", err)
	}

	cleared := a.ClearTasks()
	if cleared < 1 {
		t.Fatalf("expected >= 1 task cleared, got %d", cleared)
	}

	if len(a.ListTasks()) != 0 {
		t.Fatalf("expected 0 tasks after ClearTasks, got %d", len(a.ListTasks()))
	}

	// Test RemoveTask
	taskID3, err := a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask 3 failed: %v", err)
	}
	if err := a.RemoveTask(taskID3); err != nil {
		t.Fatalf("RemoveTask failed: %v", err)
	}
	if len(a.ListTasks()) != 0 {
		t.Fatalf("expected 0 tasks after RemoveTask, got %d", len(a.ListTasks()))
	}
}

func TestBuildSystemPromptIncludesBackgroundTasks(t *testing.T) {
	cfg := SystemPromptConfig{
		ActiveTasks: []TaskInfo{
			{
				ID:       "task_7",
				Status:   "running",
				Duration: 5 * time.Minute,
				Command:  "uvicorn src.app:app --port 8000",
			},
		},
	}

	prompt := BuildSystemPrompt(cfg)
	if !strings.Contains(prompt, "<background_tasks>") {
		t.Fatalf("expected prompt to contain <background_tasks>, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "task_7") || !strings.Contains(prompt, "uvicorn") {
		t.Fatalf("expected prompt to describe task_7 and command, got:\n%s", prompt)
	}
}

func TestKillTaskNormalizedIdAndStatus(t *testing.T) {
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		NextTaskId:    1,
	}

	var buf bytes.Buffer
	taskID, err := a.SpawnTask("sleep 60", &buf)
	if err != nil {
		t.Fatalf("SpawnTask failed: %v", err)
	}
	if taskID != "task_1" {
		t.Fatalf("expected taskID task_1, got %s", taskID)
	}

	// Should be able to query status using "1" instead of "task_1"
	status, _, err := a.GetTaskStatus("1")
	if err != nil {
		t.Fatalf("GetTaskStatus with normalized id '1' failed: %v", err)
	}
	if status != "running" {
		t.Fatalf("expected status running, got %s", status)
	}

	// Should be able to kill using "1" instead of "task_1"
	err = a.KillTask("1")
	if err != nil {
		t.Fatalf("KillTask with normalized id '1' failed: %v", err)
	}

	task := a.Tasks[taskID]
	task.mu.Lock()
	finalStatus := task.Status
	task.mu.Unlock()
	if finalStatus != "killed" {
		t.Fatalf("expected status 'killed', got %s", finalStatus)
	}

	// Wait for completion event
	select {
	case event := <-a.SystemEvents:
		if !strings.Contains(event, "task_1 finished: killed") {
			t.Fatalf("unexpected system event: %s", event)
		}
		if strings.Contains(event, "System Event:") || strings.Contains(event, "Please review") {
			t.Fatalf("system event contains old rogue prompt formatting: %s", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for task completion event")
	}
}

type mockChatUI struct {
	mockTurnLoaderUI
	buf bytes.Buffer
}

func (m *mockChatUI) GetPPWriter() io.Writer {
	return &m.buf
}

func TestToggleStreamingAndChatWriter(t *testing.T) {
	mockUI := &mockChatUI{}
	a := &Agent{
		Tasks:         make(map[string]*Task),
		SystemEvents:  make(chan string, 10),
		WorkspaceRoot: t.TempDir(),
		NextTaskId:    1,
		UI:            mockUI,
	}

	task := &Task{
		ID:             "task_1",
		Command:        "echo hello",
		Status:         "running",
		Stdout:         bytes.NewBufferString("first line\n"),
		Stderr:         new(bytes.Buffer),
		StartTime:      time.Now(),
		LastOutputTime: time.Now(),
	}
	a.Tasks["task_1"] = task

	// Toggle streaming on
	var fallbackBuf bytes.Buffer
	a.ToggleStreaming("task_1", &fallbackBuf)

	// Verify streaming message and past buffered output went to chat writer, not fallback
	chatOut := mockUI.buf.String()
	if !strings.Contains(chatOut, "[streaming output of task_1...]") {
		t.Fatalf("expected streaming notice in chat writer, got: %q", chatOut)
	}
	if !strings.Contains(chatOut, "first line") {
		t.Fatalf("expected buffered output in chat writer, got: %q", chatOut)
	}
	if fallbackBuf.Len() > 0 {
		t.Fatalf("expected fallback writer to remain empty when chat writer is active, got: %q", fallbackBuf.String())
	}

	// Write additional output through taskWriter
	tw := &taskWriter{task: task, agent: a, w: &fallbackBuf, isStderr: false}
	_, err := tw.Write([]byte("second line\n"))
	if err != nil {
		t.Fatalf("tw.Write failed: %v", err)
	}

	chatOut = mockUI.buf.String()
	if !strings.Contains(chatOut, "second line") {
		t.Fatalf("expected live output in chat writer, got: %q", chatOut)
	}

	// Toggle streaming off
	a.ToggleStreaming("task_1", &fallbackBuf)
	chatOut = mockUI.buf.String()
	if !strings.Contains(chatOut, "[stopped streaming output of task_1]") {
		t.Fatalf("expected stop notice in chat writer, got: %q", chatOut)
	}
}

