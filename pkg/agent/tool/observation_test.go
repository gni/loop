package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileObservationTracker(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "loop-obs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fot := NewFileObservationTracker()

	existingFile := filepath.Join(tmpDir, "sample.txt")
	initialContent := []byte("hello world 1.0\n")
	if err := os.WriteFile(existingFile, initialContent, 0644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}

	nonExistentFile := filepath.Join(tmpDir, "new_file.txt")

	// 1. New file creation via write should be allowed without prior read
	if err := fot.CheckMutationAllowed(nonExistentFile, false); err != nil {
		t.Errorf("expected write on non-existent file to be allowed, got: %v", err)
	}

	// 2. Editing non-existent file should fail with helpful error
	if err := fot.CheckMutationAllowed(nonExistentFile, true); err == nil {
		t.Errorf("expected edit on non-existent file to fail")
	}

	// 3. Existing file has not been read yet -> mutation must be rejected
	if err := fot.CheckMutationAllowed(existingFile, true); err == nil {
		t.Errorf("expected edit on unread existing file to be rejected")
	}
	if err := fot.CheckMutationAllowed(existingFile, false); err == nil {
		t.Errorf("expected write on unread existing file to be rejected")
	}

	// 4. Record read of existing file
	fot.RecordRead(existingFile, initialContent)

	// 5. Now mutation should be allowed
	if err := fot.CheckMutationAllowed(existingFile, true); err != nil {
		t.Errorf("expected edit after read to be allowed, got: %v", err)
	}

	// 6. External mutation occurs on disk
	modifiedContent := []byte("hello world 2.0 (external edit)\n")
	if err := os.WriteFile(existingFile, modifiedContent, 0644); err != nil {
		t.Fatalf("failed to write external modification: %v", err)
	}

	// 7. CheckMutationAllowed should now detect stale checksum (CAS failure)
	if err := fot.CheckMutationAllowed(existingFile, true); err == nil {
		t.Errorf("expected CAS failure after external modification")
	}

	// 8. Re-reading the file resolves stale state
	fot.RecordRead(existingFile, modifiedContent)
	if err := fot.CheckMutationAllowed(existingFile, true); err != nil {
		t.Errorf("expected mutation allowed after re-read, got: %v", err)
	}

	// 9. Mutating and recording mutation updates the hash
	newContent := []byte("hello world 3.0 (agent edit)\n")
	if err := os.WriteFile(existingFile, newContent, 0644); err != nil {
		t.Fatalf("failed to write new content: %v", err)
	}
	fot.RecordMutation(existingFile, newContent)

	// Subsequent edit succeeds
	if err := fot.CheckMutationAllowed(existingFile, true); err != nil {
		t.Errorf("expected mutation allowed after RecordMutation, got: %v", err)
	}
}
