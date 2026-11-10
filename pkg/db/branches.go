package db

import (
	"context"
	"fmt"
	"os"
	"strings"

	"loop/pkg/domain/session"
)

func (s *JSONLStore) Branch(sessionID, entryID string) (err error) {
	return s.BranchContext(context.Background(), sessionID, entryID)
}

// BranchContext allows cancellation while waiting for session storage.
func (s *JSONLStore) BranchContext(ctx context.Context, sessionID, entryID string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()

	filePath := s.sessionPath(sessionID)
	entries, err := readEntriesContext(ctx, filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return fmt.Errorf("entry %s not found in session %s", entryID, sessionID)
	}

	marker := session.Record{Type: "branch", ParentID: path[len(path)-1].ID}
	return s.appendRecordLocked(ctx, sessionID, marker, true)
}

func (s *JSONLStore) ForkSession(parentSessionID, entryID string) (string, error) {
	return s.ForkSessionContext(context.Background(), parentSessionID, entryID)
}

// ForkSessionContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ForkSessionContext(ctx context.Context, parentSessionID, entryID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkReady(parentSessionID); err != nil {
		return "", err
	}

	release, err := s.lockStore(ctx, true)
	if err != nil {
		return "", err
	}
	defer release()

	entries, err := readEntriesContext(ctx, s.sessionPath(parentSessionID))
	if err != nil {
		return "", fmt.Errorf("failed to read parent session file: %w", err)
	}
	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return "", fmt.Errorf("entry %s not found in session %s", entryID, parentSessionID)
	}

	newID := session.NewUUID()
	f, err := os.CreateTemp(s.core.dir, ".fork-*")
	if err != nil {
		return "", fmt.Errorf("failed to create forked session file: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return "", fmt.Errorf("failed to secure forked session file: %w", err)
	}

	header := session.SessionHeader{
		Type:          "session",
		Version:       session.CurrentSessionVersion,
		ID:            newID,
		Timestamp:     nowTimestamp(),
		ParentSession: parentSessionID,
		Cwd:           s.Workspace,
	}
	if err := writeJSONLine(f, header); err != nil {
		f.Close()
		return "", fmt.Errorf("failed to write session header: %w", err)
	}

	var prev string
	for _, e := range path {
		if err := ctx.Err(); err != nil {
			f.Close()
			return "", err
		}
		if e.ID == "" || strings.HasPrefix(e.ID, "legacy-") {
			e.ID = session.NewUUID()
		}
		e.ParentID = prev
		if err := writeJSONLine(f, e); err != nil {
			f.Close()
			return "", fmt.Errorf("failed to write forked entry: %w", err)
		}
		prev = e.ID
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), s.sessionPath(newID)); err != nil {
		return "", fmt.Errorf("failed to finalize forked session file: %w", err)
	}
	return newID, nil
}
