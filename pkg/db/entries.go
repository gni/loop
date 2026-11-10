package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

func (s *JSONLStore) entries(id string) ([]session.Record, error) {
	return s.entriesContext(context.Background(), id)
}

func (s *JSONLStore) entriesContext(ctx context.Context, id string) ([]session.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(s.sessionPath(id))
	if err != nil {
		delete(s.core.cache, id)
		return nil, err
	}
	if cached, ok := s.cached(id, info); ok && cached.complete {
		return cached.entries, nil
	}
	entries, err := readEntriesContext(ctx, s.sessionPath(id))
	if err != nil {
		delete(s.core.cache, id)
		return nil, err
	}
	cached := cacheForEntries(info, entries)
	s.cacheEntries(id, cached)
	return entries, nil
}

func (s *JSONLStore) Entries(id string) ([]session.Record, error) {
	return s.EntriesContext(context.Background(), id)
}

// EntriesContext allows cancellation while waiting for session storage.
func (s *JSONLStore) EntriesContext(ctx context.Context, id string) ([]session.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkReady(id); err != nil {
		return nil, err
	}
	release, err := s.lockStore(ctx, false)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := s.entriesContext(ctx, id)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.core.cache[id]; !ok || !cached.complete {
		// Large reads are freshly decoded and unshared. Transfer ownership
		// instead of allocating a second copy of the entire saved history.
		return entries, nil
	}
	copy := make([]session.Record, len(entries))
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		copy[i] = cloneRecord(entry)
	}
	return copy, nil
}

func (s *JSONLStore) SetName(id, name string) error {
	return s.SetNameContext(context.Background(), id, name)
}

// ErrEmptySession prevents metadata from creating a conversation without input.
var ErrEmptySession = errors.New("session has no conversation content")

// SetNameContext allows cancellation while waiting for session storage.
func (s *JSONLStore) SetNameContext(ctx context.Context, id, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := session.ValidateName(name); err != nil {
		return err
	}
	if err := s.checkReady(id); err != nil {
		return err
	}
	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	entries, err := s.entriesContext(ctx, id)
	if os.IsNotExist(err) {
		return ErrEmptySession
	}
	if err != nil {
		return err
	}
	active := false
	for _, msg := range ProjectMessages(entries) {
		if isConversationMessage(msg) {
			active = true
			break
		}
	}
	if !active {
		return ErrEmptySession
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "name" {
			if entries[i].Name == name {
				return nil
			}
			break
		}
	}
	return s.appendEntryLocked(ctx, id, session.Record{Type: "name", Name: name})
}

func (s *JSONLStore) LatestForWorkspace(workspace string) (string, error) {
	return s.LatestForWorkspaceContext(context.Background(), workspace)
}

// LatestForWorkspaceContext allows cancellation while waiting for session storage.
func (s *JSONLStore) LatestForWorkspaceContext(ctx context.Context, workspace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.core == nil {
		return "", nil
	}
	dir := s.core.dir

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	type fileCandidate struct {
		id      string
		modTime time.Time
	}
	var candidates []fileCandidate
	for _, entry := range entries {
		id, valid := validSessionEntry(entry)
		if !valid {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, fileCandidate{id: id, modTime: info.ModTime()})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		filePath := filepath.Join(dir, candidate.id+".jsonl")
		f, err := storage.OpenRegularFile(filePath)
		if err != nil {
			continue
		}
		scanner := newJSONLScanner(contextReader{ctx, f})
		var sessionWorkspace string
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var rec session.Record
			if json.Unmarshal(line, &rec) == nil && rec.Type == "session" {
				sessionWorkspace = rec.Cwd
			}
			break
		}
		scanErr := scanner.Err()
		_ = f.Close()
		if scanErr != nil {
			continue
		}

		if sessionWorkspace != "" && matchesWorkspace(sessionWorkspace, workspace) {
			messages, err := s.LoadMessagesContext(ctx, candidate.id)
			if err != nil {
				continue
			}
			for _, msg := range messages {
				if isConversationMessage(msg) {
					return candidate.id, nil
				}
			}
		}
	}

	return "", ctx.Err()
}

func matchesWorkspace(a, b string) bool {
	if a == b {
		return true
	}
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	if cleanA == cleanB {
		return true
	}
	evalA, errA := filepath.EvalSymlinks(cleanA)
	evalB, errB := filepath.EvalSymlinks(cleanB)
	return errA == nil && errB == nil && evalA == evalB
}

func (s *JSONLStore) Export(id string, w io.Writer) error {
	return s.ExportContext(context.Background(), id, w)
}

// ExportContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ExportContext(ctx context.Context, id string, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.EntriesContext(ctx, id)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := json.NewEncoder(w).Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONLStore) Import(reader io.Reader) (string, error) {
	return s.ImportContext(context.Background(), reader)
}

// ImportContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ImportContext(ctx context.Context, reader io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkReady(""); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, (64<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<20 {
		return "", fmt.Errorf("session import exceeds 64 MiB")
	}
	f, err := os.CreateTemp(s.core.dir, ".import-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	entries, err := readEntriesContext(ctx, f.Name())
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("session import is empty")
	}
	hasContent := false
	for _, msg := range ProjectMessages(entries) {
		if isConversationMessage(msg) {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return "", fmt.Errorf("session import has no conversation content")
	}
	// Resolve positional legacy IDs before replacing or adding the header.
	entries = session.NormalizeEntries(entries)
	id := session.NewUUID()
	original := ""
	cwd := s.Workspace
	if entries[0].Type == "session" {
		original = entries[0].ID
		// Anchor imported sessions to the active local workspace so external
		// JSONL payloads cannot spoof or hijack another workspace's resume target.
		entries = entries[1:]
	}
	header := session.Record{Type: "session", Version: session.CurrentSessionVersion, ID: id, Cwd: cwd, ParentSession: original, Timestamp: nowTimestamp()}
	var buffer bytes.Buffer
	if err := writeJSONLine(&buffer, header); err != nil {
		return "", err
	}
	for _, entry := range entries {
		if err := writeJSONLine(&buffer, entry); err != nil {
			return "", err
		}
	}
	release, err := s.lockStore(ctx, true)
	if err != nil {
		return "", err
	}
	defer release()
	if err := os.WriteFile(f.Name(), buffer.Bytes(), 0600); err != nil {
		return "", err
	}
	file, err := os.OpenFile(f.Name(), os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), s.sessionPath(id)); err != nil {
		return "", err
	}
	if err := s.setLatestSessionIDUnlocked(id); err != nil {
		// A failed import must not leave an unreported session behind.
		return "", errors.Join(err, os.Remove(s.sessionPath(id)))
	}
	return id, nil
}

func (s *JSONLStore) RepairTail(id string) error {
	return s.RepairTailContext(context.Background(), id)
}

// RepairTailContext allows cancellation while waiting for session storage.
func (s *JSONLStore) RepairTailContext(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(id); err != nil {
		return err
	}
	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	path := s.sessionPath(id)
	data, err := readSessionFile(ctx, path)
	if err != nil {
		return err
	}
	if _, err := readEntriesContext(ctx, path); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	end := bytes.LastIndexByte(bytes.TrimSuffix(data, []byte("\n")), '\n')
	if end < 0 {
		return fmt.Errorf("no valid records before damaged tail")
	}
	temp, err := os.CreateTemp(s.core.dir, ".repair-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(data[:end+1])
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
		return err
	}
	if _, err := readEntriesContext(ctx, temp.Name()); err != nil {
		return fmt.Errorf("corruption precedes final record: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := path + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	backupFile, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, backupErr := backupFile.Write(data)
	if err := errors.Join(backupErr, backupFile.Sync(), backupFile.Close()); err != nil {
		return fmt.Errorf("save damaged session backup: %w", err)
	}
	if err := storage.SyncDirectory(s.core.dir); err != nil {
		return fmt.Errorf("sync damaged session backup: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	s.invalidateSessionCacheLocked(id)
	return storage.SyncDirectory(s.core.dir)
}

func DefaultStore() (*JSONLStore, error) { return getStore() }
