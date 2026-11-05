package run

import (
	"context"
	"strings"
	"sync"
)

// InferenceQueue limits the local model to one active generation. A slot is
// held only for inference, never while executing tools or waiting for a child.
// Waiting callers can be cancelled without submitting a request to the server.
type InferenceQueue struct{ slot chan struct{} }

func NewInferenceQueue() *InferenceQueue { return &InferenceQueue{slot: make(chan struct{}, 1)} }

func (q *InferenceQueue) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case q.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-q.slot
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { <-q.slot }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var endpointQueues = struct {
	sync.Mutex
	queues map[string]*InferenceQueue
}{queues: make(map[string]*InferenceQueue)}

// SharedInferenceQueue shares local capacity across sessions as well as agents.
// It schedules one endpoint; it does not route or load-balance requests.
func SharedInferenceQueue(endpoint string) *InferenceQueue {
	key := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	endpointQueues.Lock()
	defer endpointQueues.Unlock()
	if queue := endpointQueues.queues[key]; queue != nil {
		return queue
	}
	queue := NewInferenceQueue()
	endpointQueues.queues[key] = queue
	return queue
}
