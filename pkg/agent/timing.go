package agent

import (
	"sync"
	"time"
)

type namespaceTiming struct {
	started time.Time
	total   time.Duration
}

// TimingRecorder accumulates completed start/end pairs without retaining samples.
// Its zero value is ready to use.
type TimingRecorder struct {
	mu         sync.Mutex
	namespaces map[string]namespaceTiming
}

// NewTimingRecorder returns an empty recorder for one agent.
func NewTimingRecorder() *TimingRecorder { return &TimingRecorder{} }

// Time records a start or end marker. A new start replaces an unfinished
// measurement; an unmatched end or unknown label is ignored.
func (r *TimingRecorder) Time(namespace, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.namespaces[namespace]
	switch label {
	case "start":
		current.started = time.Now()
	case "end":
		if current.started.IsZero() {
			return
		}
		current.total += time.Since(current.started)
		current.started = time.Time{}
	default:
		return
	}
	if r.namespaces == nil {
		r.namespaces = make(map[string]namespaceTiming)
	}
	r.namespaces[namespace] = current
}

// TotalMs returns the duration of completed measurements in a namespace.
func (r *TimingRecorder) TotalMs(namespace string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.namespaces[namespace].total.Milliseconds()
}
