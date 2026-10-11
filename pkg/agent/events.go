package agent

import (
	"sync"
	"time"

	"loop/pkg/db"
)

// ToolCallEvent is a typed event emitted before tool execution and after its result.
type ToolCallEvent struct {
	Type     string // "tool_call" (before execution) or "tool_result" (after execution)
	ToolCall db.ToolCall
	Output   string
	Err      error
}

// PermissionDecision is the allow/deny result of a permission-check hook.
type PermissionDecision struct {
	Allowed bool
	Reason  string
}

// EventBus is the typed internal event bus.
// Handlers are invoked in registration order; a denial from any handler wins.
type EventBus struct {
	mu       sync.Mutex
	handlers []eventHandler
}

// NewEventBus creates an empty bus.
func NewEventBus() *EventBus { return &EventBus{} }

// On registers a subscriber and returns an unsubscribe func.
type eventHandler struct {
	id      int
	handler func(ToolCallEvent) *PermissionDecision
}

func (b *EventBus) On(handler func(ToolCallEvent) *PermissionDecision) func() {
	b.mu.Lock()
	h := eventHandler{id: len(b.handlers), handler: handler}
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, x := range b.handlers {
			if x.id == h.id {
				b.handlers = append(b.handlers[:i], b.handlers[i+1:]...)
				return
			}
		}
	}
}

// Clear removes all subscribers.
func (b *EventBus) Clear() {
	b.mu.Lock()
	b.handlers = nil
	b.mu.Unlock()
}

// Emit dispatches an event. A handler returning a denial short-circuits with
// that reason; handler panics are contained.
func (b *EventBus) Emit(e ToolCallEvent) *PermissionDecision {
	b.mu.Lock()
	handlers := append([]eventHandler(nil), b.handlers...)
	b.mu.Unlock()

	decision := &PermissionDecision{Allowed: true}
	for _, h := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					decision.Reason = "event handler error"
				}
			}()
			if d := h.handler(e); d != nil && !d.Allowed {
				decision = d
			}
		}()
		if !decision.Allowed {
			break
		}
	}
	return decision
}

var defaultEventBus = NewEventBus()

// EventBusInstance returns the process-wide typed event bus consumed by hooks
// and by pkg/ui/interceptor.go.
func EventBusInstance() *EventBus { return defaultEventBus }

// TimingRecorder records timings for the llm, tool and
// compaction namespaces surfaced by the stats command.
type TimingRecorder struct {
	mu         sync.Mutex
	Namespaces map[string][]struct{ Label string; Ms int64 }
	last       map[string]time.Time
}

func NewTimingRecorder() *TimingRecorder {
	return &TimingRecorder{Namespaces: map[string][]struct{ Label string; Ms int64 }{}, last: map[string]time.Time{}}
}

var globalTimingRecorder = NewTimingRecorder()

// GetTimingRecorder returns the process-wide recorder.
func GetTimingRecorder() *TimingRecorder { return globalTimingRecorder }

func (r *TimingRecorder) Time(namespace, label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last[namespace].IsZero() {
		r.last[namespace] = time.Now()
		return
	}
	now := time.Now()
	r.Namespaces[namespace] = append(r.Namespaces[namespace], struct{ Label string; Ms int64 }{label, now.Sub(r.last[namespace]).Milliseconds()})
	r.last[namespace] = now
}

func (r *TimingRecorder) TotalMs(namespace string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, t := range r.Namespaces[namespace] {
		if t.Ms >= 0 {
			total += t.Ms
		}
	}
	return total
}
