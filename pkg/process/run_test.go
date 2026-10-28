package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunBoundsOutputAndCancellation(t *testing.T) {
	result, err := Run(context.Background(), Options{Executable: "sh", Arguments: []string{"-c", "printf 123456789"}, OutputLimit: 4, Timeout: time.Second})
	if err == nil || result.Stdout != "1234" || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("unbounded output: %+v %v", result, err)
	}
	started := time.Now()
	_, err = Run(context.Background(), Options{Executable: "sh", Arguments: []string{"-c", "sleep 30 & wait"}, Timeout: 30 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatalf("process cancellation failed: %v %v", time.Since(started), err)
	}
}
