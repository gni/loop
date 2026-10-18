package db

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

func (s *JSONLStore) ClearSession(sessionID string) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.sessionPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	latestPath := s.latestSessionPath()
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == sessionID {
		_ = os.Remove(latestPath)
	}
	return nil
}

func (s *JSONLStore) ClearHistory() error {
	if err := s.checkReady(""); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read sessions directory: %w", err)
	}

	for _, entry := range entries {
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		if err := os.Remove(s.sessionPath(sessionID)); err != nil {
			return fmt.Errorf("failed to remove session file %s: %w", entry.Name(), err)
		}
	}

	_ = os.Remove(s.latestSessionPath())
	return nil
}

func (s *JSONLStore) SetLatestSessionID(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLatestSessionIDUnlocked(sessionID)
}

func (s *JSONLStore) setLatestSessionIDUnlocked(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	now := time.Now()
	_ = os.Chtimes(s.sessionPath(sessionID), now, now)
	return s.writeLatestID(sessionID)
}

func (s *JSONLStore) writeLatestID(sessionID string) error {
	f, err := os.CreateTemp(s.dir, ".latest-session-*")
	if err != nil {
		return fmt.Errorf("create latest-session pointer: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fmt.Errorf("secure latest-session pointer: %w", err)
	}
	if _, err := f.WriteString(sessionID); err != nil {
		f.Close()
		return fmt.Errorf("write latest-session pointer: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync latest-session pointer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close latest-session pointer: %w", err)
	}
	if err := os.Rename(f.Name(), s.latestSessionPath()); err != nil {
		return fmt.Errorf("replace latest-session pointer: %w", err)
	}
	return nil
}

func (s *JSONLStore) GetLatestSessionID() (string, error) {
	if err := s.checkReady(""); err != nil {
		return "", err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if data, err := os.ReadFile(s.latestSessionPath()); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" && session.ValidateID(id) == nil {
			if fi, err := os.Stat(s.sessionPath(id)); err == nil && !fi.IsDir() {
				return id, nil
			}
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		name, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = name
		}
	}

	return latestFile, nil
}

func (s *JSONLStore) GetSessions() ([]session.SessionInfo, error) {
	return s.GetSessionsSorted(false)
}

func (s *JSONLStore) GetSessionsSorted(ascending bool) ([]session.SessionInfo, error) {
	if err := s.checkReady(""); err != nil {
		return nil, err
	}

	s.mu.RLock()
	entries, err := os.ReadDir(s.dir)
	s.mu.RUnlock()

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []session.SessionInfo

	for _, entry := range entries {
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}

		f, err := os.Open(s.sessionPath(sessionID))
		if err != nil {
			continue
		}

		var firstTimestamp string
		var msgCount, totalTokens int
		var previewParts []string

		scanner := newJSONLScanner(f)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var record session.Record
			if json.Unmarshal(line, &record) != nil {
				continue
			}

			if isSkippedSlashRecord(record.Message) {
				continue
			}

			msgCount++
			if firstTimestamp == "" {
				firstTimestamp = record.Timestamp
			}
			totalTokens += record.Message.PromptTokens + record.Message.CompletionTokens

			if record.Message.Role != message.RoleSystem && record.Message.Content != "" {
				previewParts = append(previewParts, record.Message.Content)
			}
		}
		f.Close()

		if msgCount == 0 {
			continue
		}
		if firstTimestamp == "" {
			firstTimestamp = entryModTimeOrNow(entry)
		}

		preview := "(No text content)"
		if len(previewParts) > 0 {
			cleanText := strings.Join(strings.Fields(strings.Join(previewParts, " ")), " ")
			if runes := []rune(cleanText); len(runes) > 60 {
				preview = string(runes[:60]) + "..."
			} else {
				preview = cleanText
			}
		}

		sessions = append(sessions, session.SessionInfo{
			SessionID:   sessionID,
			Timestamp:   firstTimestamp,
			MsgCount:    msgCount,
			Preview:     preview,
			TotalTokens: totalTokens,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if ascending {
			return sessions[i].Timestamp < sessions[j].Timestamp
		}
		return sessions[i].Timestamp > sessions[j].Timestamp
	})

	return sessions, nil
}

func (s *JSONLStore) RenameSession(oldID, newID string) error {
	if err := session.ValidateID(oldID); err != nil {
		return fmt.Errorf("invalid old session ID: %w", err)
	}
	if err := s.checkReady(newID); err != nil {
		return fmt.Errorf("invalid new session ID: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldPath := s.sessionPath(oldID)
	newPath := s.sessionPath(newID)

	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("source session does not exist: %w", err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("target session %s already exists", newID)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename session file: %w", err)
	}

	latestPath := s.latestSessionPath()
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == oldID {
		if err := s.writeLatestID(newID); err != nil {
			return fmt.Errorf("session renamed, but latest pointer update failed: %w", err)
		}
	}
	return nil
}

func readEntries(filePath string) ([]session.Record, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []session.Record
	scanner := newJSONLScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record session.Record
		if json.Unmarshal(line, &record) == nil {
			entries = append(entries, record)
		}
	}
	return entries, scanner.Err()
}

func (s *JSONLStore) Branch(sessionID, entryID string) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.sessionPath(sessionID)
	entries, err := readEntries(filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return fmt.Errorf("entry %s not found in session %s", entryID, sessionID)
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	marker := session.Record{
		ID:        session.NewUUID(),
		ParentID:  path[len(path)-1].ID,
		Type:      "branch",
		Timestamp: nowTimestamp(),
	}
	if marker.ParentID == "" {
		for _, e := range entries {
			if e.Type == "message" {
				marker.ParentID = e.ID
			}
		}
	}
	if err := writeJSONLine(f, marker); err != nil {
		return fmt.Errorf("failed to write branch marker: %w", err)
	}
	return s.setLatestSessionIDUnlocked(sessionID)
}

func (s *JSONLStore) ForkSession(parentSessionID, entryID string) (string, error) {
	if err := s.checkReady(parentSessionID); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := readEntries(s.sessionPath(parentSessionID))
	if err != nil {
		return "", fmt.Errorf("failed to read parent session file: %w", err)
	}
	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return "", fmt.Errorf("entry %s not found in session %s", entryID, parentSessionID)
	}

	newID := session.NewUUID()
	f, err := os.OpenFile(s.sessionPath(newID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to create forked session file: %w", err)
	}
	defer f.Close()

	header := session.SessionHeader{
		Type:          "session",
		Version:       session.CurrentSessionVersion,
		ID:            newID,
		Timestamp:     nowTimestamp(),
		ParentSession: parentSessionID,
	}
	if err := writeJSONLine(f, header); err != nil {
		return "", fmt.Errorf("failed to write session header: %w", err)
	}

	var prev string
	for _, e := range path {
		e.ID = session.NewUUID()
		e.ParentID = prev
		e.Type = "message"
		if err := writeJSONLine(f, e); err != nil {
			return "", fmt.Errorf("failed to write forked entry: %w", err)
		}
		prev = e.ID
	}

	return newID, nil
}

func Branch(sessionID, entryID string) error {
	return withStore(func(s *JSONLStore) error { return s.Branch(sessionID, entryID) })
}

func ForkSession(parentSessionID, entryID string) (string, error) {
	return withStoreVal(func(s *JSONLStore) (string, error) { return s.ForkSession(parentSessionID, entryID) })
}

func ClearHistory() error {
	return withStore(func(s *JSONLStore) error { return s.ClearHistory() })
}

func ClearSession(sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.ClearSession(sessionID) })
}

func SetLatestSessionID(sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.SetLatestSessionID(sessionID) })
}

func GetLatestSessionID() (string, error) {
	return withStoreVal(func(s *JSONLStore) (string, error) { return s.GetLatestSessionID() })
}

func GetSessions() ([]SessionInfo, error) {
	return withStoreVal(func(s *JSONLStore) ([]SessionInfo, error) { return s.GetSessions() })
}

func entryModTimeOrNow(entry os.DirEntry) string {
	if info, err := entry.Info(); err == nil {
		return info.ModTime().Format("2006-01-02 15:04:05")
	}
	return nowTimestamp()
}
