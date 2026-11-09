package agent

import (
	"sync"

	"loop/pkg/db"
)

// ToolPhase is a single tool call's execution state, independent of UI output.
type ToolPhase string

const (
	ToolPhaseStreaming ToolPhase = "streaming"
	ToolPhaseReady     ToolPhase = "ready"
	ToolPhaseRunning   ToolPhase = "running"
	ToolPhaseCompleted ToolPhase = "completed"
	ToolPhaseFailed    ToolPhase = "failed"
	ToolPhaseCancelled ToolPhase = "cancelled"
)

// ToolLifecycleEvent identifies an operation by a stable local ID. ProviderID
// is populated after the provider's final response is available. Neither
// arguments nor results are copied into lifecycle events.
type ToolLifecycleEvent struct {
	CallID        string    `json:"call_id"`
	ProviderID    string    `json:"provider_id,omitempty"`
	Index         int       `json:"index"`
	Name          string    `json:"name"`
	Phase         ToolPhase `json:"phase"`
	ArgumentBytes int       `json:"argument_bytes,omitempty"`
}

// ToolCallView is the immutable snapshot consumers use to render a tool's state.
// ToolCall.Index is the execution order, not a provider stream block index.
type ToolCallView struct {
	ToolLifecycleEvent
	// Full arguments and output remain in memory only. Public lifecycle events
	// contain metadata, not repository content or tool results.
	Arguments string
	Output    string
	Error     string
}

// ToolBatch connects model stream events to execution without controlling either
// of them. A local ID survives provisional streaming, validation and completion.
// Only finalized provider calls are executable.
type ToolBatch struct {
	mu     sync.Mutex
	calls  []*ToolCallView
	notify func(ToolLifecycleEvent)
}

func NewToolBatch(notify func(ToolLifecycleEvent)) *ToolBatch {
	return &ToolBatch{notify: notify}
}

func (b *ToolBatch) ensure(index int) *ToolCallView {
	for len(b.calls) <= index {
		b.calls = append(b.calls, nil)
	}
	if b.calls[index] == nil {
		b.calls[index] = &ToolCallView{ToolLifecycleEvent: ToolLifecycleEvent{
			CallID: db.NewUUID(), Index: index, Phase: ToolPhaseStreaming,
		}}
	}
	return b.calls[index]
}

func (b *ToolBatch) change(index int, update func(*ToolCallView)) {
	if b == nil || index < 0 {
		return
	}
	b.mu.Lock()
	call := b.ensure(index)
	update(call)
	event := call.ToolLifecycleEvent
	notify := b.notify
	b.mu.Unlock()
	if notify != nil {
		notify(event)
	}
}

// Observe registers a streamed tool and counts its argument fragments. No
// attempt is made to execute JSON until the provider returns its final calls.
func (b *ToolBatch) Observe(index int, name, argumentDelta string) {
	b.change(index, func(c *ToolCallView) {
		if name != "" {
			c.Name = name
		}
		c.ArgumentBytes += len(argumentDelta)
	})
}

// remap retains stable call IDs when provisional arrival order changes.
func (b *ToolBatch) remap(mapping map[int]int, count int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	reordered := make([]*ToolCallView, count)
	var cancelled []ToolLifecycleEvent
	for old, call := range b.calls {
		if call == nil {
			continue
		}
		if index, ok := mapping[old]; ok && index >= 0 && index < count {
			call.Index = index
			reordered[index] = call
		} else {
			event := call.ToolLifecycleEvent
			event.Phase = ToolPhaseCancelled
			cancelled = append(cancelled, event)
		}
	}
	b.calls = reordered
	notify := b.notify
	b.mu.Unlock()
	if notify != nil {
		for _, event := range cancelled {
			notify(event)
		}
	}
}

// Seal reconciles streamed calls with authoritative final calls. In particular,
// a missing or malformed preview must never change the execution order.
func (b *ToolBatch) Seal(calls []db.ToolCall) {
	if b == nil {
		return
	}
	for i, tc := range calls {
		b.change(i, func(c *ToolCallView) {
			c.ProviderID = tc.ID
			c.Name = tc.Function.Name
			c.Arguments = tc.Function.Arguments
			if c.Phase == ToolPhaseStreaming {
				c.Phase = ToolPhaseReady
			}
			if c.ArgumentBytes == 0 {
				c.ArgumentBytes = len(tc.Function.Arguments)
			}
		})
	}
	b.mu.Lock()
	extra := make([]int, 0)
	for i := len(calls); i < len(b.calls); i++ {
		if b.calls[i] != nil && b.calls[i].Phase == ToolPhaseStreaming {
			extra = append(extra, i)
		}
	}
	b.mu.Unlock()
	for _, i := range extra {
		b.transition(i, ToolPhaseCancelled)
	}
}

func (b *ToolBatch) transition(index int, phase ToolPhase) {
	if b == nil || index < 0 {
		return
	}
	b.mu.Lock()
	call := b.ensure(index)
	if call.Phase == phase || call.Phase == ToolPhaseCompleted ||
		call.Phase == ToolPhaseFailed || call.Phase == ToolPhaseCancelled {
		b.mu.Unlock()
		return
	}
	call.Phase = phase
	event, notify := call.ToolLifecycleEvent, b.notify
	b.mu.Unlock()
	if notify != nil {
		notify(event)
	}
}

// CancelUnfinished closes every provisional or unexecuted call on provider
// failure, user abort, or interruption, without fabricating tool results.
func (b *ToolBatch) CancelUnfinished() {
	if b == nil {
		return
	}
	b.mu.Lock()
	indices := make([]int, 0, len(b.calls))
	for i, call := range b.calls {
		if call != nil && call.Phase != ToolPhaseCompleted &&
			call.Phase != ToolPhaseFailed && call.Phase != ToolPhaseCancelled {
			indices = append(indices, i)
		}
	}
	b.mu.Unlock()
	for _, i := range indices {
		b.transition(i, ToolPhaseCancelled)
	}
}

// SetResult stores the final, already-pruned output for the presentation layer.
// It never includes that output in emitted lifecycle metadata.
func (b *ToolBatch) SetResult(index int, output string, err error) {
	if b == nil || index < 0 {
		return
	}
	b.mu.Lock()
	call := b.ensure(index)
	call.Output = output
	if err != nil {
		call.Error = err.Error()
	}
	b.mu.Unlock()
}

func (b *ToolBatch) Start(index int) { b.transition(index, ToolPhaseRunning) }
func (b *ToolBatch) Finish(index int, failed bool) {
	if failed {
		b.transition(index, ToolPhaseFailed)
	} else {
		b.transition(index, ToolPhaseCompleted)
	}
}

func (b *ToolBatch) CancelByProviderID(providerID string) {
	if b == nil || providerID == "" {
		return
	}
	b.mu.Lock()
	ids := make([]int, 0, 1)
	for i, call := range b.calls {
		if call != nil && call.ProviderID == providerID {
			ids = append(ids, i)
		}
	}
	b.mu.Unlock()
	for _, i := range ids {
		b.transition(i, ToolPhaseCancelled)
	}
}
