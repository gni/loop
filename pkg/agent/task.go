package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"loop/pkg/domain/limits"
)

var MaxTrackedTasks = limits.DefaultMaxTrackedTasks

type Task struct {
	ID             string
	Command        string
	Status         string
	Stdout         *bytes.Buffer
	Stderr         *bytes.Buffer
	Cmd            *exec.Cmd
	Pgid           int
	StartTime      time.Time
	EndTime        time.Time
	Err            error
	mu             sync.Mutex
	LastOutputTime time.Time
}

type TaskInfo struct {
	ID       string
	Command  string
	Status   string
	Duration time.Duration
	BytesOut int
}

type taskWriter struct {
	task     *Task
	agent    *Agent
	w        io.Writer
	isStderr bool
}

func (a *Agent) resolveTaskOutputWriter(fallback io.Writer) io.Writer {
	if a != nil {
		if chatWriter := a.GetChatWriter(); chatWriter != nil && chatWriter != os.Stderr {
			return chatWriter
		}
	}
	return fallback
}

func (tw *taskWriter) Write(p []byte) (n int, err error) {
	tw.task.mu.Lock()
	const maxLen = 100 * 1024
	targetBuf := tw.task.Stdout
	if tw.isStderr {
		targetBuf = tw.task.Stderr
	}
	targetBuf.Write(p)
	if targetBuf.Len() > maxLen {
		targetBuf.Next(targetBuf.Len() - maxLen)
	}
	tw.task.LastOutputTime = time.Now()
	tw.task.mu.Unlock()

	tw.agent.TasksMu.Lock()
	isStreaming := tw.agent.StreamingTask == tw.task.ID
	tw.agent.TasksMu.Unlock()

	if isStreaming {
		return tw.agent.resolveTaskOutputWriter(tw.w).Write(p)
	}

	return len(p), nil
}

func (a *Agent) findTaskLocked(id string) (string, *Task) {
	if task, exists := a.Tasks[id]; exists {
		return id, task
	}
	if !strings.HasPrefix(id, "task_") {
		prefixed := "task_" + id
		if task, exists := a.Tasks[prefixed]; exists {
			return prefixed, task
		}
	}
	return id, nil
}

func (a *Agent) lookupTask(id string) (string, *Task, error) {
	a.TasksMu.Lock()
	resolvedID, task := a.findTaskLocked(id)
	a.TasksMu.Unlock()
	if task == nil {
		return resolvedID, nil, errors.New(RuntimeMessagef("task_not_found", "task %s not found", resolvedID))
	}
	return resolvedID, task, nil
}

func killTaskProcessLocked(task *Task) {
	task.Status = "killed"
	task.EndTime = time.Now()
	if task.Pgid > 0 {
		_ = syscall.Kill(-task.Pgid, syscall.SIGKILL)
	}
	if task.Cmd != nil && task.Cmd.Process != nil {
		_ = task.Cmd.Process.Kill()
	}
}

func (a *Agent) SpawnTask(command string, w io.Writer) (string, error) {
	a.TasksMu.Lock()
	id := fmt.Sprintf("task_%d", a.NextTaskId)
	a.NextTaskId++

	task := &Task{
		ID:             id,
		Command:        command,
		Status:         "running",
		Stdout:         new(bytes.Buffer),
		Stderr:         new(bytes.Buffer),
		StartTime:      time.Now(),
		LastOutputTime: time.Now(),
	}
	if a.Tasks == nil {
		a.Tasks = make(map[string]*Task)
	}
	a.Tasks[id] = task
	a.pruneFinishedTasksLocked()
	if len(a.Tasks) > limits.MaxTrackedTasks() {
		delete(a.Tasks, id)
		a.TasksMu.Unlock()
		return "", fmt.Errorf("maximum number of tracked tasks (%d) reached; remove or finish a task before spawning another", limits.MaxTrackedTasks())
	}
	task.mu.Lock()
	a.TasksMu.Unlock()

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	task.Cmd = cmd
	cmd.Stdout = &taskWriter{task: task, agent: a, w: w, isStderr: false}
	cmd.Stderr = &taskWriter{task: task, agent: a, w: w, isStderr: true}

	if err := cmd.Start(); err != nil {
		task.Status = "failed"
		task.EndTime = time.Now()
		task.Err = err
		task.mu.Unlock()
		return id, fmt.Errorf("start background task %s: %w", id, err)
	}
	if cmd.Process != nil {
		task.Pgid = cmd.Process.Pid
	}
	task.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		task.mu.Lock()
		task.EndTime = time.Now()
		if task.Status == "running" {
			if waitErr != nil {
				task.Status = "failed"
				task.Err = waitErr
			} else {
				task.Status = "completed"
			}
		}
		finalStatus := task.Status
		task.mu.Unlock()

		a.TasksMu.Lock()
		if a.StreamingTask == task.ID {
			a.StreamingTask = ""
			dest := a.resolveTaskOutputWriter(w)
			fmt.Fprintf(dest, "\n[task %s finished with status: %s]\n", task.ID, finalStatus)
		}
		a.TasksMu.Unlock()

		select {
		case a.SystemEvents <- fmt.Sprintf("Background task %s finished: %s", task.ID, finalStatus):
		default:
		}
	}()

	return id, nil
}

func (a *Agent) KillTask(id string) error {
	resolvedID, task, err := a.lookupTask(id)
	if err != nil {
		return err
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if task.Status != "running" {
		return fmt.Errorf("task %s is not running (status: %s)", resolvedID, task.Status)
	}
	killTaskProcessLocked(task)
	return nil
}

func (a *Agent) RemoveTask(id string) error {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	resolvedID, task := a.findTaskLocked(id)
	if task == nil {
		return errors.New(RuntimeMessagef("task_not_found", "task %s not found", resolvedID))
	}

	task.mu.Lock()
	if task.Status == "running" {
		killTaskProcessLocked(task)
	}
	task.mu.Unlock()

	delete(a.Tasks, resolvedID)
	return nil
}

func (a *Agent) ResolveTaskID(id string) string {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()
	resolvedID, _ := a.findTaskLocked(id)
	return resolvedID
}

func (a *Agent) GetTaskStatus(id string) (string, string, error) {
	_, task, err := a.lookupTask(id)
	if err != nil {
		return "", "", err
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	var sb bytes.Buffer
	writeTail := func(b []byte, maxLen int) {
		if len(b) == 0 {
			return
		}
		if len(b) > maxLen {
			fmt.Fprintf(&sb, "...(truncated %d bytes)...\n", len(b)-maxLen)
			sb.Write(b[len(b)-maxLen:])
		} else {
			sb.Write(b)
		}
		sb.WriteString("\n")
	}

	writeTail(task.Stdout.Bytes(), 4000)
	writeTail(task.Stderr.Bytes(), 4000)

	output := sb.String()
	if output == "" {
		output = "(no output)"
	}
	return task.Status, output, nil
}

func (a *Agent) GetTaskCommand(id string) string {
	_, task, err := a.lookupTask(id)
	if err != nil {
		return ""
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.Command
}

func (a *Agent) ListTasks() []TaskInfo {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	list := make([]TaskInfo, 0, len(a.Tasks))
	for _, t := range a.Tasks {
		t.mu.Lock()
		end := t.EndTime
		if end.IsZero() {
			end = time.Now()
		}
		list = append(list, TaskInfo{
			ID:       t.ID,
			Command:  t.Command,
			Status:   t.Status,
			Duration: end.Sub(t.StartTime),
			BytesOut: t.Stdout.Len() + t.Stderr.Len(),
		})
		t.mu.Unlock()
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (a *Agent) ToggleStreaming(id string, w io.Writer) {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	dest := a.resolveTaskOutputWriter(w)
	resolvedID, task := a.findTaskLocked(id)
	if a.StreamingTask == resolvedID && resolvedID != "" {
		a.StreamingTask = ""
		fmt.Fprintf(dest, "\n[stopped streaming output of %s]\n", resolvedID)
		return
	}

	if task == nil {
		fmt.Fprintf(dest, "\nError: %s\n", RuntimeMessagef("task_not_found", "task %s not found", resolvedID))
		return
	}

	task.mu.Lock()
	isRunning := task.Status == "running"
	task.mu.Unlock()
	if isRunning {
		a.StreamingTask = resolvedID
		fmt.Fprintf(dest, "\n[streaming output of %s...]\n", resolvedID)
	} else {
		fmt.Fprintf(dest, "\n[recorded output of %s]\n", resolvedID)
	}

	task.mu.Lock()
	outBytes := append([]byte(nil), task.Stdout.Bytes()...)
	errBytes := append([]byte(nil), task.Stderr.Bytes()...)
	task.mu.Unlock()

	if len(outBytes) > 0 {
		_, _ = dest.Write(outBytes)
	}
	if len(errBytes) > 0 {
		_, _ = dest.Write(errBytes)
	}
}

func (a *Agent) GetLastRunningTaskId() string {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	var lastRunningId string
	var lastTime time.Time
	for _, t := range a.Tasks {
		t.mu.Lock()
		isRunning := t.Status == "running"
		startTime := t.StartTime
		t.mu.Unlock()

		if isRunning && (lastRunningId == "" || startTime.After(lastTime)) {
			lastRunningId = t.ID
			lastTime = startTime
		}
	}
	return lastRunningId
}

func (a *Agent) CountActiveTasks() int {
	if a == nil {
		return 0
	}
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()
	count := 0
	for _, t := range a.Tasks {
		t.mu.Lock()
		if t.Status == "running" {
			count++
		}
		t.mu.Unlock()
	}
	return count
}

func (a *Agent) KillAllTasks() int {
	if a == nil {
		return 0
	}
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()

	killed := 0
	for _, t := range a.Tasks {
		t.mu.Lock()
		if t.Status == "running" {
			killTaskProcessLocked(t)
			killed++
		}
		t.mu.Unlock()
	}
	return killed
}

func (a *Agent) ClearTasks() int {
	if a == nil {
		return 0
	}
	killed := a.KillAllTasks()
	a.TasksMu.Lock()
	a.Tasks = make(map[string]*Task)
	a.StreamingTask = ""
	a.TasksMu.Unlock()
	return killed
}

func (a *Agent) pruneFinishedTasksLocked() {
	if a == nil || len(a.Tasks) <= limits.MaxTrackedTasks() {
		return
	}

	terminal := make([]*Task, 0, len(a.Tasks))
	for _, t := range a.Tasks {
		t.mu.Lock()
		switch t.Status {
		case "completed", "failed", "killed":
			terminal = append(terminal, t)
		}
		t.mu.Unlock()
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].EndTime.Before(terminal[j].EndTime) })

	for _, t := range terminal {
		if len(a.Tasks) <= limits.MaxTrackedTasks() {
			return
		}
		delete(a.Tasks, t.ID)
		if a.StreamingTask == t.ID {
			a.StreamingTask = ""
		}
	}
}
