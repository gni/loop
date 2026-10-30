package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

type cachedEntries struct {
	size     int64
	modified time.Time
	entries  []session.Record
}

func (s *JSONLStore) lock(exclusive bool) (func(), error) {
	return storage.Lock(s.dir+"/.store.lock", exclusive)
}

func (s *JSONLStore) entries(id string) ([]session.Record, error) {
	info, err := os.Stat(s.sessionPath(id))
	if err != nil {
		return nil, err
	}
	if cached, ok := s.cache[id]; ok && cached.size == info.Size() && cached.modified.Equal(info.ModTime()) {
		return cached.entries, nil
	}
	entries, err := readEntries(s.sessionPath(id))
	if err != nil {
		return nil, err
	}
	s.cache[id] = cachedEntries{size: info.Size(), modified: info.ModTime(), entries: entries}
	return entries, nil
}

func (s *JSONLStore) Entries(id string) ([]session.Record, error) {
	if err := s.checkReady(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := s.entries(id)
	if err != nil {
		return nil, err
	}
	copy := make([]session.Record, len(entries))
	for i, entry := range entries {
		copy[i] = entry
		copy[i].Data = append(entry.Data[:0:0], entry.Data...)
		copy[i].Message = message.Clone(entry.Message)
		copy[i].Messages = message.CloneAll(entry.Messages)
	}
	return copy, nil
}

func (s *JSONLStore) SetName(id, name string) error {
	if len(name) > 256 {
		return fmt.Errorf("session name exceeds 256 bytes")
	}
	return s.AppendEntry(id, session.Record{Type: "name", Name: name})
}

func (s *JSONLStore) LatestForWorkspace(workspace string) (string, error) {
	infos, err := s.GetSessions()
	if err != nil {
		return "", err
	}
	var selected string
	var modified time.Time
	for _, info := range infos {
		if info.Cwd != workspace {
			continue
		}
		stat, err := os.Stat(s.sessionPath(info.SessionID))
		if err == nil && stat.ModTime().After(modified) {
			selected, modified = info.SessionID, stat.ModTime()
		}
	}
	return selected, nil
}

func (s *JSONLStore) Export(id string, w io.Writer) error {
	entries, err := s.Entries(id)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := json.NewEncoder(w).Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONLStore) Import(reader io.Reader) (string, error) {
	if err := s.checkReady(""); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 64<<20 {
		return "", fmt.Errorf("session import exceeds 64 MiB")
	}
	f, err := os.CreateTemp(s.dir, ".import-*")
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
	entries, err := readEntries(f.Name())
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("session import is empty")
	}
	id := session.NewUUID()
	original := ""
	cwd := s.Workspace
	if entries[0].Type == "session" {
		original = entries[0].ID
		cwd = entries[0].Cwd
		entries = entries[1:]
	}
	header := session.Record{Type: "session", Version: session.CurrentSessionVersion, ID: id, Cwd: cwd, ParentSession: original, Timestamp: nowTimestamp()}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	if err := encoder.Encode(header); err != nil {
		return "", err
	}
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lock(true)
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
	if err := os.Rename(f.Name(), s.sessionPath(id)); err != nil {
		return "", err
	}
	return id, s.setLatestSessionIDUnlocked(id)
}

func (s *JSONLStore) RepairTail(id string) error {
	if err := s.checkReady(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()
	path := s.sessionPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := readEntries(path); err == nil {
		return nil
	}
	end := bytes.LastIndexByte(bytes.TrimSuffix(data, []byte("\n")), '\n')
	if end < 0 {
		return fmt.Errorf("no valid records before damaged tail")
	}
	temp, err := os.CreateTemp(s.dir, ".repair-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.Write(data[:end+1])
	if err := errors.Join(writeErr, temp.Sync(), temp.Close()); err != nil {
		return err
	}
	if _, err := readEntries(temp.Name()); err != nil {
		return fmt.Errorf("corruption precedes final record: %w", err)
	}
	backup := path + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.WriteFile(backup, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	delete(s.cache, id)
	return nil
}

func DefaultStore() (*JSONLStore, error) { return getStore() }
