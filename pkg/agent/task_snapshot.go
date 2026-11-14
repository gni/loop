package agent

import (
	"errors"
	"os/exec"
	"unicode/utf8"

	"loop/pkg/domain/evidence"
	domaintool "loop/pkg/domain/tool"
)

const taskStatusTailBytes = 4000

// GetTaskOutputSnapshot keeps output positions and execution evidence consistent
// with the reported status, even when the process finishes during a poll.
func (a *Agent) GetTaskOutputSnapshot(id string, full bool) (*domaintool.BackgroundTaskSnapshot, error) {
	id, task, err := a.lookupTask(id)
	if err != nil {
		return nil, err
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	maxBytes := taskStatusTailBytes
	if full {
		maxBytes = 0
	}
	stdout, _ := taskOutputTail(task.Stdout.Bytes(), maxBytes)
	stderr, _ := taskOutputTail(task.Stderr.Bytes(), maxBytes)
	return &domaintool.BackgroundTaskSnapshot{
		ID: id, Command: task.Command, Status: task.Status,
		Stdout: string(stdout), Stderr: string(stderr),
		StdoutBytes: max(task.stdoutBytes, int64(task.Stdout.Len())),
		StderrBytes: max(task.stderrBytes, int64(task.Stderr.Len())),
		StartedAt:   task.StartTime.UnixNano(), Diagnostic: taskDiagnostic(task.Err),
		Evidence: task.evidenceLocked(),
	}, nil
}

func taskOutputTail(data []byte, maxBytes int) ([]byte, int) {
	start := 0
	if maxBytes > 0 {
		start = max(0, len(data)-maxBytes)
	}
	for start > 0 && start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return data[start:], start
}

func taskDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return "command exited, but output capture did not finish before the deadline; process-group cleanup was attempted"
	}
	return err.Error()
}

// evidenceLocked requires the task mutex, also used by output and completion.
func (task *Task) evidenceLocked() *evidence.Record {
	outcome := evidence.Unknown
	switch task.Status {
	case "running":
		outcome = evidence.Started
	case "completed":
		outcome = evidence.Succeeded
	case "failed":
		outcome = evidence.Failed
	}
	record := &evidence.Record{Kind: "command", Target: task.Command, Directory: task.Directory, TaskID: task.ID, Outcome: outcome}
	if task.ExitCode != nil {
		code := *task.ExitCode
		record.ExitCode = &code
	}
	if outcome != evidence.Started {
		if check := evidence.CommandCheck(task.Command, record.ExitCode, outcome); check != nil {
			check.Target = task.Directory
			check.StartedAt = task.StartTime.UnixNano()
			if check.Kind == "test" {
				check.ObserveOutput(task.Stdout.String() + "\n" + task.Stderr.String())
			}
			record.Checks = []evidence.Check{*check}
		}
	}
	return record
}
