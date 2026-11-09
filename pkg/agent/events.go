package agent

import (
	"sync"

	"loop/pkg/agent/run"
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
	nextID   int
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
	h := eventHandler{id: b.nextID, handler: handler}
	b.nextID++
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, x := range b.handlers {
				if x.id == h.id {
					b.handlers = append(b.handlers[:i], b.handlers[i+1:]...)
					return
				}
			}
		})
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
					decision = &PermissionDecision{Allowed: false, Reason: "event handler error"}
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

// RunObserver forwards shared-runner lifecycle events to the session event sink.
// Presentation adapters and headless consumers receive the same event contract.
func (a *Agent) RunObserver(sessionID string) func(run.Event) error {
	return func(event run.Event) error {
		return a.sendEvent(Event{Type: event.Type, SessionID: sessionID,
			Message: event.Message, Data: map[string]int{"step": event.Step}})
	}
}
