package lifecycle

import (
	"os"
	"sync"
	"testing"
	"time"
)

// Shutdown must record the code it decided on so the caller can run its own deferred
// cleanup instead of being cut short by os.Exit, and hooks must not double-fire when a
// double interrupt re-enters the shutdown path.
func TestShutdownRecordsCodeAndRunsHooksOnce(t *testing.T) {
	var mu sync.Mutex
	hookCalls := 0
	exitCalls := 0

	sup := NewSupervisor(Config{
		DoubleInterruptDuration: 500 * time.Millisecond,
		ExitFunc: func(code int) {
			mu.Lock()
			exitCalls++
			mu.Unlock()
		},
	})

	sup.OnShutdown(func() {
		mu.Lock()
		hookCalls++
		mu.Unlock()
	})

	// First shutdown: a recording exit func returns so defers in the caller still run.
	sup.SetExitFunc(func(code int) {
		mu.Lock()
		exitCalls++
		mu.Unlock()
	})

	sup.TriggerSignal(os.Interrupt)

	mu.Lock()
	if hookCalls != 1 {
		t.Fatalf("hook ran %d times, want 1", hookCalls)
	}
	if exitCalls != 1 {
		t.Fatalf("exit func called %d times, want 1", exitCalls)
	}
	if code := sup.ExitCode(); code != 130 {
		t.Fatalf("recorded exit code %d, want 130", code)
	}
	mu.Unlock()

	select {
	case <-sup.Context().Done():
	default:
		t.Fatal("context not canceled on shutdown")
	}

	// Re-entered shutdown: hooks stay quiet, the exit func still fires so a double
	// interrupt terminates the process.
	sup.TriggerSignal(os.Interrupt)

	mu.Lock()
	defer mu.Unlock()
	if hookCalls != 1 {
		t.Fatalf("hook ran %d times on re-entry, want 1", hookCalls)
	}
	if exitCalls != 2 {
		t.Fatalf("exit func called %d times on re-entry, want 2", exitCalls)
	}
}
