package lifecycle

import (
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorLIFOCleanup(t *testing.T) {
	var executionOrder []int
	var exitCode int

	sup := NewSupervisor(Config{
		ExitFunc: func(code int) {
			exitCode = code
		},
	})

	sup.OnShutdown(func() {
		executionOrder = append(executionOrder, 1)
	})
	sup.OnShutdown(func() {
		executionOrder = append(executionOrder, 2)
	})
	sup.OnShutdown(func() {
		executionOrder = append(executionOrder, 3)
	})

	sup.TriggerSignal(syscall.SIGTERM)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	if len(executionOrder) != 3 {
		t.Fatalf("expected 3 hooks executed, got %d", len(executionOrder))
	}

	if executionOrder[0] != 3 || executionOrder[1] != 2 || executionOrder[2] != 1 {
		t.Fatalf("expected LIFO execution order [3, 2, 1], got %v", executionOrder)
	}

	select {
	case <-sup.Context().Done():
		// Success: context was canceled
	default:
		t.Fatal("expected supervisor context to be canceled after shutdown")
	}
}

func TestSupervisorInterceptedCancel(t *testing.T) {
	exited := false
	canceledCount := 0

	sup := NewSupervisor(Config{
		DoubleInterruptDuration: 50 * time.Millisecond,
		ExitFunc: func(code int) {
			exited = true
		},
	})

	sup.OnCancel(func() bool {
		canceledCount++
		return true // Absorb the interrupt
	})

	// First interrupt: should be absorbed
	sup.TriggerSignal(os.Interrupt)
	if exited {
		t.Fatal("expected interrupt to be absorbed by OnCancel handler")
	}
	if canceledCount != 1 {
		t.Fatalf("expected cancel handler called once, got %d", canceledCount)
	}

	// Wait past double-interrupt duration
	time.Sleep(70 * time.Millisecond)

	// Second interrupt after window: should be absorbed again
	sup.TriggerSignal(os.Interrupt)
	if exited {
		t.Fatal("expected second interrupt after timeout window to be absorbed")
	}
	if canceledCount != 2 {
		t.Fatalf("expected cancel handler called twice, got %d", canceledCount)
	}
}

func TestSupervisorDoubleInterruptForceExit(t *testing.T) {
	var exitCode int
	var mu sync.Mutex

	sup := NewSupervisor(Config{
		DoubleInterruptDuration: 500 * time.Millisecond,
		ExitFunc: func(code int) {
			mu.Lock()
			exitCode = code
			mu.Unlock()
		},
	})

	sup.OnCancel(func() bool {
		return true // Attempt to absorb
	})

	// First interrupt: absorbed
	sup.TriggerSignal(os.Interrupt)

	// Second interrupt immediately (within 500ms): should bypass handler and force exit 130
	sup.TriggerSignal(os.Interrupt)

	mu.Lock()
	defer mu.Unlock()
	if exitCode != 130 {
		t.Fatalf("expected double interrupt to force exit 130, got %d", exitCode)
	}
}
