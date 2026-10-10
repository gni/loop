package agent

import (
	"context"
	"testing"
	"time"
)

// Regression test for "sometimes it just stops": a provider that sends chunks on a
// channel whose consumer has stopped draining must not park forever. Before the fix
// a plain `chunkChan <- chunk` blocked indefinitely when the loop broke out early
// (approval prompt, cancellation, subagent cap), so the error channel never received
// and the caller waited on <-streamErrChan with no timeout.
func TestEmitChunkNeverBlocksForever(t *testing.T) {
	// Buffered channel already full, no consumer.
	chunkChan := make(chan StreamChunk, 1)
	chunkChan <- StreamChunk{Type: "text", Content: "filler"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		emitChunk(ctx, chunkChan, StreamChunk{Type: "reasoning", Content: "dropped"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emitChunk blocked on a full channel with no consumer")
	}
}

// Cancellation must unblock a pending send immediately rather than after the timeout.
func TestEmitChunkUnblocksOnCancel(t *testing.T) {
	chunkChan := make(chan StreamChunk)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	emitChunk(ctx, chunkChan, StreamChunk{Type: "text", Content: "x"})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled send took %v; expected prompt unblock", elapsed)
	}
}

// A dropped chunk must be reported so the UI can surface truncated output instead of
// silently losing it, and the agent-side counter must observe it through the hook.
func TestEmitChunkReportsDrops(t *testing.T) {
	chunkChan := make(chan StreamChunk, 1)
	chunkChan <- StreamChunk{Type: "text", Content: "filler"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "text", Content: "dropped"}, nil) {
		t.Fatal("send to a full channel reported success")
	}

	a := NewAgent(nil, "", nil)
	if a.DroppedChunks() != 0 {
		t.Fatalf("fresh agent reports %d dropped chunks", a.DroppedChunks())
	}

	observed := 0
	provider := &OpenAICompatibleProvider{ChunkDropObserver: func() { observed++; a.recordDroppedChunk() }}
	emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "text", Content: "dropped"}, provider.ChunkDropObserver)

	if observed != 1 || a.DroppedChunks() != 1 || a.TurnDroppedChunks() != 1 {
		t.Fatalf("observed=%d dropped=%d turn=%d, want 1/1/1", observed, a.DroppedChunks(), a.TurnDroppedChunks())
	}

	// Delivered chunks must not be counted as drops.
	<-chunkChan
	if !emitChunkObserved(ctx, chunkChan, StreamChunk{Type: "text", Content: "ok"}, provider.ChunkDropObserver) {
		t.Fatal("drained channel reported a drop")
	}
	if a.DroppedChunks() != 1 {
		t.Fatalf("delivered chunk incremented counter to %d", a.DroppedChunks())
	}
}

// Normal streaming still delivers chunks in order when a consumer is draining.
func TestEmitChunkDeliversWhenDrained(t *testing.T) {
	chunkChan := make(chan StreamChunk, 4)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		emitChunk(ctx, chunkChan, StreamChunk{Type: "text", Content: "chunk"})
	}
	close(chunkChan)

	var got int
	for range chunkChan {
		got++
	}
	if got != 4 {
		t.Fatalf("delivered %d chunks, want 4", got)
	}
}
