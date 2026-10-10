package tool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneSpillDirRemovesExpiredFiles(t *testing.T) {
	dir := t.TempDir()

	old := time.Now().Add(-2 * SpillFileMaxAge)
	if err := os.WriteFile(filepath.Join(dir, "old.log"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "old.log"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fresh.log"), []byte("kept"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := PruneSpillDir(dir, 10, SpillFileMaxAge); err != nil {
		t.Fatalf("prune error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "old.log")); !os.IsNotExist(err) {
		t.Fatal("expired spill file was not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.log")); err != nil {
		t.Fatalf("fresh spill file was removed: %v", err)
	}
}

func TestPruneSpillDirEvictsOldestOverCap(t *testing.T) {
	dir := t.TempDir()

	base := time.Now().Add(-10 * time.Minute)
	names := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".log")
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}

	if err := PruneSpillDir(dir, 3, 0); err != nil {
		t.Fatalf("prune error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("kept %d files, want 3", len(entries))
	}

	for _, evicted := range names[:2] {
		if _, err := os.Stat(evicted); !os.IsNotExist(err) {
			t.Fatalf("oldest file %s survived eviction", filepath.Base(evicted))
		}
	}
	for _, kept := range names[2:] {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("newest file %s was evicted instead of the oldest", filepath.Base(kept))
		}
	}
}

func TestSpillRetentionCapAppliesOnWrite(t *testing.T) {
	dir := t.TempDir()
	session := "retention"
	spillDir := filepath.Join(dir, ".loop", "spill", session)

	base := time.Now().Add(-2 * SpillFileMaxAge)
	for i := 0; i < 3; i++ {
		name := filepath.Join(spillDir, "stale"+string(rune('a'+i))+".log")
		if err := os.MkdirAll(spillDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, base, base); err != nil {
			t.Fatal(err)
		}
	}

	_, path, pruned := SpillAndPruneOutput(dir, session, "call-1", "bash", "x\ny\nz", 1)
	if !pruned {
		t.Fatal("oversized output should have been pruned")
	}
	if path == "" {
		t.Fatal("expected spill path")
	}

	entries, err := os.ReadDir(spillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("retention pass left %d files, want only the new one", len(entries))
	}
	if entries[0].Name() != filepath.Base(path) {
		t.Fatalf("unexpected surviving file %s", entries[0].Name())
	}
}
