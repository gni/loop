package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpillAndPruneOutput(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "loop-spill-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Case 1: Short output under threshold -> no pruning
	shortOut := "Everything is fine. 3 tests passed."
	pruned, spillPath, wasPruned := SpillAndPruneOutput(tmpDir, "sess-1", "tc-1", "bash", shortOut, 8192)
	if wasPruned {
		t.Errorf("expected wasPruned=false for short output")
	}
	if pruned != shortOut {
		t.Errorf("expected pruned to equal shortOut, got: %s", pruned)
	}
	if spillPath != "" {
		t.Errorf("expected spillPath to be empty, got: %s", spillPath)
	}

	// Case 2: Multi-line output exceeding threshold -> pruned and spilled to disk
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString(fmt.Sprintf("log line %03d: running build step and checking output data...\n", i))
	}
	longOut := sb.String()

	prunedLong, spillPathLong, wasPrunedLong := SpillAndPruneOutput(tmpDir, "sess-1", "tc-2", "bash", longOut, 1024)
	if !wasPrunedLong {
		t.Errorf("expected wasPruned=true for long output")
	}
	if !strings.Contains(prunedLong, "[... ") || !strings.Contains(prunedLong, "omitted to protect context window") {
		t.Errorf("expected prune marker in output: %s", prunedLong)
	}
	if !strings.Contains(prunedLong, "log line 001:") {
		t.Errorf("expected head lines in pruned output")
	}
	if !strings.Contains(prunedLong, "log line 200:") {
		t.Errorf("expected tail lines in pruned output")
	}

	// Verify the spill file exists on disk and contains the exact original content
	fullDiskPath := filepath.Join(tmpDir, spillPathLong)
	data, err := os.ReadFile(fullDiskPath)
	if err != nil {
		t.Fatalf("failed to read spill file from disk at %s: %v", fullDiskPath, err)
	}
	if string(data) != longOut {
		t.Errorf("spill file content mismatch: got %d bytes, want %d bytes", len(data), len(longOut))
	}

	// Case 3: Giant single line exceeding threshold
	giantLine := strings.Repeat("A", 12000)
	prunedGiant, _, wasPrunedGiant := SpillAndPruneOutput(tmpDir, "sess-1", "tc-3", "bash", giantLine, 4096)
	if !wasPrunedGiant {
		t.Errorf("expected wasPruned=true for giant line")
	}
	if !strings.Contains(prunedGiant, "omitted to protect context window") {
		t.Errorf("expected prune marker for giant line")
	}
}
