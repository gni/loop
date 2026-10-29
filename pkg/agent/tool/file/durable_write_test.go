package file

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWritePreservesOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.md")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(target, []byte("updated"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "updated" {
		t.Fatalf("replacement failed: %q, %v", body, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions incorrect: %v %v", info, err)
	}
	if err := AtomicWriteFile(filepath.Join(target, "nested"), []byte("invalid"), 0600); err == nil {
		t.Fatal("expected a failed write when parent is not a directory")
	}
	retained, err := os.ReadFile(target)
	if err != nil || string(retained) != "updated" {
		t.Fatalf("failed write damaged existing file: %q %v", retained, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file not cleaned: %v %v", entries, err)
	}
}
