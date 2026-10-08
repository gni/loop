package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdvisoryRepeatReminderWithCanonicalArguments(t *testing.T) {
	guard := NewTurnExecutionGuard()

	// 1. Two calls with identical JSON but different key ordering:
	call1Args := `{"query": "auth", "path": "pkg/auth"}`
	call2Args := `{"path": "pkg/auth", "query": "auth"}`

	// Call 1
	if err := guard.CheckPreExecution("grep", call1Args); err != nil {
		t.Fatalf("unexpected check error on call 1: %v", err)
	}
	guard.RecordPostExecution("grep", call1Args, "matches found", nil)
	if rem := guard.GetAdvisoryReminder("grep", call1Args); rem != "" {
		t.Fatalf("expected no reminder on call 1, got: %s", rem)
	}

	// Call 2 (different key order, but semantically identical)
	if err := guard.CheckPreExecution("grep", call2Args); err != nil {
		t.Fatalf("unexpected check error on call 2: %v", err)
	}
	guard.RecordPostExecution("grep", call2Args, "matches found", nil)
	if rem := guard.GetAdvisoryReminder("grep", call2Args); rem != "" {
		t.Fatalf("expected no reminder on call 2, got: %s", rem)
	}

	// Call 3: should trigger gentle reminder
	if err := guard.CheckPreExecution("grep", call1Args); err != nil {
		t.Fatalf("unexpected check error on call 3: %v", err)
	}
	guard.RecordPostExecution("grep", call1Args, "matches found", nil)
	rem3 := guard.GetAdvisoryReminder("grep", call1Args)
	if !strings.Contains(rem3, "Advisory Reminder") || !strings.Contains(rem3, "repeating the exact same tool call") {
		t.Fatalf("expected gentle reminder on call 3, got: %q", rem3)
	}

	// Call 4
	guard.RecordPostExecution("grep", call1Args, "matches found", nil)

	// Call 5: should trigger detailed reminder
	guard.RecordPostExecution("grep", call1Args, "matches found", nil)
	rem5 := guard.GetAdvisoryReminder("grep", call1Args)
	if !strings.Contains(rem5, "Advisory Loop Warning") || !strings.Contains(rem5, "consecutive_calls: 5") {
		t.Fatalf("expected detailed reminder on call 5, got: %q", rem5)
	}

	// Interleaving with a different call resets the streak
	diffArgs := `{"path": "pkg/auth", "query": "tokens"}`
	guard.RecordPostExecution("grep", diffArgs, "different matches", nil)
	if rem := guard.GetAdvisoryReminder("grep", diffArgs); rem != "" {
		t.Fatalf("expected streak reset on different call, got: %s", rem)
	}
}

func TestSandboxSymlinkEscapePrevention(t *testing.T) {
	tempWorkspace := t.TempDir()
	outsideDir := t.TempDir()

	secretFile := filepath.Join(outsideDir, "secret.key")
	if err := os.WriteFile(secretFile, []byte("super-secret-token"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside the workspace pointing outside
	linkPath := filepath.Join(tempWorkspace, "symlink_escape")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Skip("Symlink creation not supported or permitted on this filesystem")
	}

	a := &Agent{WorkspaceRoot: tempWorkspace}

	// Accessing through the symlink must be blocked by SafePath
	_, err := a.SafePath("symlink_escape")
	if err == nil {
		t.Fatal("expected security violation error for symlink pointing outside workspace root, got nil")
	}
	if !strings.Contains(err.Error(), "security violation") || !strings.Contains(err.Error(), "resolves via symlink outside workspace root") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
