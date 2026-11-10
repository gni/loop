package run

import (
	"context"
	"strings"
	"sync"
)

// InferenceQueue limits the local model to one active generation. A slot is
// held only for inference, never while executing tools or waiting for a child.
// Waiting callers can be cancelled without submitting a request to the server.
type InferenceQueue struct {
	slot     chan struct{}
	endpoint string
	shared   bool
}

func NewInferenceQueue() *InferenceQueue { return &InferenceQueue{slot: make(chan struct{}, 1)} }

func (q *InferenceQueue) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q.shared {
		return acquireEndpoint(ctx, q.endpoint)
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
	queues map[string]*endpointQueue
}{queues: make(map[string]*endpointQueue)}

type endpointQueue struct {
	queue *InferenceQueue
	users int
}

// SharedInferenceQueue shares local capacity across sessions as well as agents.
// It schedules one endpoint; it does not route or load-balance requests.
func SharedInferenceQueue(endpoint string) *InferenceQueue {
	key := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return &InferenceQueue{endpoint: key, shared: true}
}

func acquireEndpoint(ctx context.Context, key string) (func(), error) {
	endpointQueues.Lock()
	entry := endpointQueues.queues[key]
	if entry == nil {
		entry = &endpointQueue{queue: NewInferenceQueue()}
		endpointQueues.queues[key] = entry
	}
	entry.users++
	endpointQueues.Unlock()
	detach := func() {
		endpointQueues.Lock()
		entry.users--
		if entry.users == 0 {
			delete(endpointQueues.queues, key)
		}
		endpointQueues.Unlock()
	}
	release, err := entry.queue.Acquire(ctx)
	if err != nil {
		detach()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { release(); detach() }) }, nil
}
