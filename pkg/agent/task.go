package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	bashtool "loop/pkg/agent/tool/bash"
	"loop/pkg/domain/output"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/process"
)

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
	filtered bytes.Buffer
	filter   *output.SafeShellOutputWriter
}

func (a *Agent) resolveTaskOutputWriter(fallback io.Writer) io.Writer {
	if a != nil {
		if chatWriter := a.GetChatWriter(); chatWriter != nil && chatWriter != os.Stderr {
			return chatWriter
		}
	}
	if fallback == nil {
		return io.Discard
	}
	return fallback
}

func (tw *taskWriter) Write(p []byte) (n int, err error) {
	tw.agent.TasksMu.Lock()
	tw.task.mu.Lock()
	targetBuf := tw.task.Stdout
	if tw.isStderr {
		targetBuf = tw.task.Stderr
	}
	appendTaskOutput(targetBuf, p)
	if tw.filter == nil {
		tw.filter = output.NewSafeShellOutputWriter(&tw.filtered)
	}
	_, _ = tw.filter.Write(p)
	filtered := append([]byte(nil), tw.filtered.Bytes()...)
	tw.filtered.Reset()
	tw.task.LastOutputTime = time.Now()
	tw.task.mu.Unlock()

	drain := false
	if tw.agent.StreamingTask == tw.task.ID && tw.agent.Tasks[tw.task.ID] == tw.task {
		drain = tw.agent.queueTaskOutputLocked(tw.w, filtered)
	}
	tw.agent.TasksMu.Unlock()
	if drain {
		tw.agent.drainTaskOutput()
	}

	return len(p), nil
}

func appendTaskOutput(buffer *bytes.Buffer, data []byte) {
	const maxBytes = 100 * 1024
	if len(data) >= maxBytes {
		buffer.Reset()
		data = data[len(data)-maxBytes:]
	} else if keep := maxBytes - len(data); buffer.Len() > keep {
		tail := buffer.Bytes()[buffer.Len()-keep:]
		buffer.Reset()
		_, _ = buffer.Write(tail)
	}
	_, _ = buffer.Write(data)
	// Retention may cut through a rune, including one split across writes.
	retained := buffer.Bytes()
	start := 0
	for start < len(retained) && !utf8.RuneStart(retained[start]) {
		start++
	}
	buffer.Next(start)
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
		_ = process.KillPgid(task.Pgid)
	}
	if task.Cmd != nil {
		_ = process.KillProcessGroup(task.Cmd)
	}
}

func (a *Agent) SpawnTask(command string, w io.Writer) (string, error) {
	return a.SpawnTaskContext(a.Context(), command, w)
}

// SpawnTaskContext uses caller cancellation up to process acceptance. Accepted
// background work keeps the agent lifetime rather than the completed turn's.
func (a *Agent) SpawnTaskContext(ctx context.Context, command string, w io.Writer) (string, error) {
	return a.SpawnTaskWithOptions(ctx, command, w, domaintool.BackgroundTaskOptions{})
}

// SpawnTaskWithOptions preserves shell source while setting the process directory
// and environment. It shares the same acceptance and teardown rules as SpawnTask.
func (a *Agent) SpawnTaskWithOptions(ctx context.Context, command string, w io.Writer, options domaintool.BackgroundTaskOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("background command is empty")
	}
	if !a.IsShellExecutionAllowed() {
		return "", fmt.Errorf("background shell execution requires a trusted workspace")
	}
	if a.Lifetime != nil && a.Lifetime.Err() != nil {
		return "", fmt.Errorf("agent already disposed: %w", a.Lifetime.Err())
	}
	a.stateMu.Lock()
	sessionID, generation := a.activeSessionID, a.sessionGeneration
	a.stateMu.Unlock()
	maxTasks := a.GetLimits().MaxTrackedTasks
	shellCommand := command
	environment := a.GetExecutionEnv()
	workspace := environment["LOOP_WORKSPACE"]
	directory := workspace
	if options.Directory != "" {
		var err error
		directory, err = a.SafePath(options.Directory)
		if err != nil {
			return "", fmt.Errorf("invalid background working directory: %w", err)
		}
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			return "", fmt.Errorf("background working directory is unavailable: %s", directory)
		}
	}
	for key, value := range options.Environment {
		environment[key] = value
	}
	if a.IsShellScanDefaultsEnabled() {
		shellCommand = bashtool.WithScanDefaults(command, workspace)
	}
	cmd := bashtool.NewCommand(shellCommand)
	cmd.Dir = directory
	cmd.WaitDelay = 2 * time.Second
	for key, value := range environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	a.TasksMu.Lock()
	if err := a.checkTaskContext(ctx, sessionID, generation); err != nil {
		a.TasksMu.Unlock()
		return "", err
	}
	id := fmt.Sprintf("task_%d", a.NextTaskID)
	a.NextTaskID++

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
	a.pruneFinishedTasksLocked(maxTasks)
	if len(a.Tasks) > maxTasks {
		delete(a.Tasks, id)
		a.TasksMu.Unlock()
		return "", fmt.Errorf("maximum number of tracked tasks (%d) reached; remove or finish a task before spawning another", maxTasks)
	}
	task.mu.Lock()
	a.TasksMu.Unlock()

	task.Cmd = cmd
	cmd.Stdout = &taskWriter{task: task, agent: a, w: w, isStderr: false}
	cmd.Stderr = &taskWriter{task: task, agent: a, w: w, isStderr: true}

	if err := a.checkTaskContext(ctx, sessionID, generation); err != nil {
		task.Status, task.Err, task.EndTime = "failed", err, time.Now()
		task.mu.Unlock()
		a.TasksMu.Lock()
		if a.Tasks[id] == task {
			delete(a.Tasks, id)
		}
		a.TasksMu.Unlock()
		return "", err
	}
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

	// Background subprocesses must not survive agent lifetime cancellation.
	stopWatcher := make(chan struct{})
	var lifetimeDone <-chan struct{}
	if a.Lifetime != nil {
		lifetimeDone = a.Lifetime.Done()
	}
	go func() {
		select {
		case <-lifetimeDone:
			task.mu.Lock()
			if task.Status == "running" {
				killTaskProcessLocked(task)
			}
			task.mu.Unlock()
		case <-stopWatcher:
		}
	}()
	go func() {
		waitErr := cmd.Wait()
		// Once the tracked command exits, descendants must not outlive it or
		// keep inherited stdout/stderr pipes open indefinitely.
		_ = process.KillProcessGroup(cmd)
		close(stopWatcher)
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
		tracked := a.Tasks[task.ID] == task
		wasStreaming := tracked && a.StreamingTask == task.ID
		if wasStreaming {
			a.StreamingTask = ""
		}
		drain := false
		if wasStreaming {
			drain = a.queueTaskOutputLocked(w, []byte(fmt.Sprintf("\n[task %s finished with status: %s]\n", task.ID, finalStatus)))
		}
		a.TasksMu.Unlock()
		if drain {
			a.drainTaskOutput()
		}

		if tracked && a.checkTaskContext(context.Background(), sessionID, generation) == nil {
			select {
			case a.SystemEvents <- fmt.Sprintf("Background task %s finished: %s", task.ID, finalStatus):
			default:
			}
		}
	}()

	return id, nil
}

func (a *Agent) checkTaskContext(ctx context.Context, sessionID string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Lifetime != nil {
		if err := a.Lifetime.Err(); err != nil {
			return fmt.Errorf("agent already disposed: %w", err)
		}
	}
	a.stateMu.Lock()
	currentID, currentGeneration := a.activeSessionID, a.sessionGeneration
	a.stateMu.Unlock()
	if currentID != sessionID || currentGeneration != generation {
		return fmt.Errorf("active session changed before background task started")
	}
	return nil
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
	if a.StreamingTask == resolvedID {
		a.StreamingTask = ""
	}
	return nil
}

func (a *Agent) ResolveTaskID(id string) string {
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()
	resolvedID, _ := a.findTaskLocked(id)
	return resolvedID
}

func (a *Agent) GetTaskStatus(id string) (string, string, error) {
	return a.taskStatus(id, 4000)
}

// GetFullTaskStatus returns all output still retained by the task buffers.
func (a *Agent) GetFullTaskStatus(id string) (string, string, error) {
	return a.taskStatus(id, 0)
}

func (a *Agent) taskStatus(id string, maxBytes int) (string, string, error) {
	_, task, err := a.lookupTask(id)
	if err != nil {
		return "", "", err
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	var sb bytes.Buffer
	writeTail := func(b []byte) {
		start := 0
		if maxBytes > 0 {
			start = max(0, len(b)-maxBytes)
		}
		// A byte limit must not split the first UTF-8 character in the tail.
		for start > 0 && start < len(b) && !utf8.RuneStart(b[start]) {
			start++
		}
		if start > 0 {
			fmt.Fprintf(&sb, "...(truncated %d bytes)...\n", start)
		}
		sb.Write(b[start:])
	}
	writeTail(task.Stdout.Bytes())
	if task.Stdout.Len() > 0 && task.Stderr.Len() > 0 && !bytes.HasSuffix(task.Stdout.Bytes(), []byte{'\n'}) {
		sb.WriteByte('\n')
	}
	writeTail(task.Stderr.Bytes())
	if task.Err != nil {
		if sb.Len() > 0 && !bytes.HasSuffix(sb.Bytes(), []byte{'\n'}) {
			sb.WriteByte('\n')
		}
		diagnostic := task.Err.Error()
		if errors.Is(task.Err, exec.ErrWaitDelay) {
			diagnostic = "command exited, but a child kept output pipes open; remaining child processes were stopped"
		}
		fmt.Fprintf(&sb, "[task error: %s]", diagnostic)
	}

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
	resolvedID, task := a.findTaskLocked(id)
	var notice string
	var outBytes, errBytes []byte
	switch {
	case a.StreamingTask == resolvedID && resolvedID != "":
		a.StreamingTask = ""
		notice = fmt.Sprintf("\n[stopped streaming output of %s]\n", resolvedID)
	case task == nil:
		notice = fmt.Sprintf("\nError: %s\n", RuntimeMessagef("task_not_found", "task %s not found", resolvedID))
	default:
		task.mu.Lock()
		if task.Status == "running" {
			a.StreamingTask = resolvedID
			notice = fmt.Sprintf("\n[streaming output of %s...]\n", resolvedID)
		} else {
			notice = fmt.Sprintf("\n[recorded output of %s]\n", resolvedID)
		}
		outBytes = append([]byte(nil), task.Stdout.Bytes()...)
		errBytes = append([]byte(nil), task.Stderr.Bytes()...)
		task.mu.Unlock()
	}
	var frame bytes.Buffer
	frame.WriteString(notice)
	if len(outBytes) > 0 {
		_, _ = output.NewSafeShellOutputWriter(&frame).Write(outBytes)
	}
	if len(errBytes) > 0 {
		_, _ = output.NewSafeShellOutputWriter(&frame).Write(errBytes)
	}
	drain := a.queueTaskOutputLocked(w, append([]byte(nil), frame.Bytes()...))
	a.TasksMu.Unlock()
	if drain {
		a.drainTaskOutput()
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
	return a.killAllTasksLocked()
}

// The registry lock prevents tasks from being added while they are stopped.
func (a *Agent) killAllTasksLocked() int {
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
	a.TasksMu.Lock()
	defer a.TasksMu.Unlock()
	killed := a.killAllTasksLocked()
	a.stateMu.Lock()
	a.sessionGeneration++
	a.stateMu.Unlock()
	a.Tasks = make(map[string]*Task)
	a.StreamingTask = ""
	clear(a.taskOutput.queue)
	a.taskOutput.queue = nil
	a.taskOutput.bytes = 0
	a.taskOutput.dropped = false
	return killed
}

func (a *Agent) pruneFinishedTasksLocked(maxTasks int) {
	if a == nil || len(a.Tasks) <= maxTasks {
		return
	}

	// Wait can update EndTime after a task has been killed. Sort snapshots,
	// rather than reading mutable task fields outside their lock.
	type finishedTask struct {
		id  string
		end time.Time
	}
	terminal := make([]finishedTask, 0, len(a.Tasks))
	for _, t := range a.Tasks {
		t.mu.Lock()
		switch t.Status {
		case "completed", "failed", "killed":
			terminal = append(terminal, finishedTask{id: t.ID, end: t.EndTime})
		}
		t.mu.Unlock()
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].end.Before(terminal[j].end) })

	for _, t := range terminal {
		if len(a.Tasks) <= maxTasks {
			return
		}
		delete(a.Tasks, t.id)
		if a.StreamingTask == t.id {
			a.StreamingTask = ""
		}
	}
}

// GetTaskOutputVersion distinguishes unchanged output from identical new data
// in a full rolling buffer. The process writer updates LastOutputTime per write.
func (a *Agent) GetTaskOutputVersion(id string) int64 {
	a.TasksMu.Lock()
	_, t := a.findTaskLocked(id)
	a.TasksMu.Unlock()
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.LastOutputTime.IsZero() {
		return 0
	}
	return t.LastOutputTime.UnixNano()
}
