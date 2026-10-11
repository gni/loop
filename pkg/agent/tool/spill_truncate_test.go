package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
)

func TestSpillPruneNoticeIsActionable(t *testing.T) {
	tmpDir := t.TempDir()

	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		sb.WriteString("log line " + strings.Repeat("é", 300) + " step " + itoa(i) + "\n")
	}
	long := sb.String()

	pruned, spillPath, wasPruned := SpillAndPruneOutput(tmpDir, "sess-2", "tc-9", "bash", long, 8192)
	if !wasPruned {
		t.Fatalf("expected pruning")
	}

	// Head and tail lines are complete (never partial), the marker states the
	// exact next offset and the total line count, and the retained text stays
	// valid UTF-8.
	if !utf8.ValidString(pruned) {
		t.Errorf("pruned output is not valid UTF-8")
	}
	if !strings.Contains(pruned, "omitted to protect context window") {
		t.Errorf("missing prune marker: %s", pruned)
	}
	if !strings.Contains(pruned, "total lines") || !strings.Contains(pruned, "offset=26") {
		t.Errorf("prune notice is not actionable: %s", pruned)
	}
	if !strings.Contains(pruned, ".loop/spill/") {
		t.Errorf("prune notice must state where the full output lives: %s", pruned)
	}

	// The spilled file still holds the exact unpruned bytes.
	data, err := os.ReadFile(filepath.Join(tmpDir, spillPath))
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != long {
		t.Errorf("spill content mismatch")
	}
}

func TestSpillPruneRespectsResolvedCeilings(t *testing.T) {
	tmpDir := t.TempDir()
	out := strings.Repeat("x", 60000)

	// A caller asking for a larger budget is clamped to the safety ceiling, so
	// oversized output is still pruned.
	pruned, _, wasPruned := SpillAndPruneOutput(tmpDir, "sess-3", "tc-10", "bash", out, limits.MaxOutputBytes()+100000)
	if !wasPruned {
		t.Fatalf("expected pruning under the resolved ceiling")
	}
	if len(pruned) > limits.MaxOutputBytes()+file.RuneCount(file.SpillBytePruneNotice(0, 0, 0, 0))+file.RuneCount(file.SpillFullOutputNotice(""))+200 {
		t.Errorf("pruned output exceeded the resolved budget: %d bytes", len(pruned))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
