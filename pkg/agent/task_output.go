package agent

import (
	"fmt"
	"io"
	"sync"
	"unicode/utf8"
)

// backgroundTasks keeps process ownership and its bounded presentation queue
// together. TasksMu protects both; terminal writes happen outside that lock.
type backgroundTasks struct {
	Tasks         map[string]*Task
	TasksMu       sync.Mutex
	NextTaskID    int
	StreamingTask string
	taskOutput    taskOutputState
}

// TasksMu protects the queue. One caller drains it outside all task locks,
// keeping snapshots, stdout, stderr and completion notices in display order.
type taskOutputState struct {
	queue      []taskOutputChunk
	bytes      int
	draining   bool
	dropped    bool
	generation uint64
}

type taskOutputChunk struct {
	writer     io.Writer
	data       []byte
	generation uint64
}

func (a *Agent) queueTaskOutputLocked(w io.Writer, data []byte) bool {
	if len(data) == 0 || (a.Lifetime != nil && a.Lifetime.Err() != nil) {
		return false
	}
	// A slow terminal must not allow live previews to grow without bound.
	// Retained command output stays in the task's separate stdout/stderr buffers.
	const maxQueued = 256 * 1024
	if len(data) > maxQueued {
		data = data[len(data)-maxQueued:]
		for len(data) > 0 && !utf8.RuneStart(data[0]) {
			data = data[1:]
		}
		// Do not retain the oversized backing allocation through a small tail.
		data = append([]byte(nil), data...)
		a.taskOutput.dropped = true
	}
	for a.taskOutput.bytes+len(data) > maxQueued && len(a.taskOutput.queue) > 0 {
		a.taskOutput.bytes -= len(a.taskOutput.queue[0].data)
		a.taskOutput.queue[0] = taskOutputChunk{}
		a.taskOutput.queue = a.taskOutput.queue[1:]
		a.taskOutput.dropped = true
	}
	a.taskOutput.queue = append(a.taskOutput.queue, taskOutputChunk{writer: w, data: data, generation: a.taskOutput.generation})
	a.taskOutput.bytes += len(data)
	if a.taskOutput.draining {
		return false
	}
	a.taskOutput.draining = true
	return true
}

func (a *Agent) drainTaskOutput() {
	for {
		a.TasksMu.Lock()
		if a.Lifetime != nil && a.Lifetime.Err() != nil {
			clear(a.taskOutput.queue)
			a.taskOutput.queue = nil
			a.taskOutput.bytes = 0
			a.taskOutput.dropped = false
		}
		if len(a.taskOutput.queue) == 0 {
			a.taskOutput.draining = false
			a.TasksMu.Unlock()
			return
		}
		chunk := a.taskOutput.queue[0]
		a.taskOutput.queue[0] = taskOutputChunk{}
		a.taskOutput.queue = a.taskOutput.queue[1:]
		a.taskOutput.bytes -= len(chunk.data)
		dropped := a.taskOutput.dropped
		a.taskOutput.dropped = false
		a.TasksMu.Unlock()
		dest := a.resolveTaskOutputWriter(chunk.writer)
		a.TasksMu.Lock()
		current := chunk.generation == a.taskOutput.generation && (a.Lifetime == nil || a.Lifetime.Err() == nil)
		a.TasksMu.Unlock()
		if !current {
			continue
		}
		if dropped {
			_, _ = fmt.Fprintln(dest, "\n[live preview skipped older output; retained output is available with /task view]")
		}
		// Display errors must not turn a successfully running command into a failure.
		_, _ = dest.Write(chunk.data)
	}
}
